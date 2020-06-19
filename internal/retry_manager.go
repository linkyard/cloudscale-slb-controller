package cloudscale_slb_controller

import (
	"fmt"
	log "github.com/sirupsen/logrus"
	"k8s.io/api/core/v1"
	"sync"
	"time"
)

type RetryManager interface {
	IsEndpointsAffectedByRetry(namespace string, name string) bool
	IsAffectedByRetry(svc *v1.Service, actionType ActionType, wasDeleted bool) bool
	SubmitForUpdate(svc *v1.Service, actionType ActionType, wasDeleted bool)
	Start()
	Stop()
}

type RetryEntry struct {
	Service      *v1.Service
	ActionType   ActionType
	WasDeleted   bool
	BlockedUntil time.Time
	Retries      int
}

type DefaultRetryManager struct {
	mutex      *sync.Mutex
	entries    map[string]RetryEntry
	done       chan struct{}
	wg         sync.WaitGroup
	processor  *EventProcessor
	repository Repository
	running    bool
}

func NewRetryManager(processor *EventProcessor, repository Repository) *DefaultRetryManager {
	return &DefaultRetryManager{
		mutex:      &sync.Mutex{},
		entries:    make(map[string]RetryEntry),
		done:       make(chan struct{}),
		wg:         sync.WaitGroup{},
		processor:  processor,
		repository: repository,
		running:    false,
	}
}

func (m *DefaultRetryManager) IsEndpointsAffectedByRetry(namespace string, name string) bool {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	_, ok := m.entries[getKeyFor(namespace, name)]
	return ok
}

func (m *DefaultRetryManager) IsAffectedByRetry(svc *v1.Service, actionType ActionType, wasDeleted bool) bool {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	entry, ok := m.entries[getKey(svc)]
	if ok {
		log.WithFields(log.Fields{
			"svc": getKey(svc),
		}).Debug("updating entry for service")
		if actionType == Ignore && entry.ActionType == CreateIp {
			log.WithFields(log.Fields{
				"svc": getKey(svc),
			}).Info("removing service from retry queue because it is no longer relevant")
			delete(m.entries, getKey(svc))
		} else if actionType != Ignore {
			m.entries[getKey(svc)] = RetryEntry{
				Service:      svc,
				ActionType:   actionType,
				WasDeleted:   wasDeleted,
				BlockedUntil: entry.BlockedUntil,
				Retries:      entry.Retries,
			}
		}
	}
	return ok
}

func (m *DefaultRetryManager) SubmitForUpdate(svc *v1.Service, actionType ActionType, wasDeleted bool) {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	log.WithFields(log.Fields{
		"svc": getKey(svc),
	}).Debug("submitting for retry")
	m.entries[getKey(svc)] = RetryEntry{
		Service:      svc,
		ActionType:   actionType,
		WasDeleted:   wasDeleted,
		BlockedUntil: getNextRetryTime(0),
		Retries:      0,
	}
}

func getKey(svc *v1.Service) string {
	return fmt.Sprintf("%v/%v", svc.Namespace, svc.Name)
}

func getKeyFor(namespace string, name string) string {
	return fmt.Sprintf("%v/%v", namespace, name)
}

func (m *DefaultRetryManager) Start() {
	log.Debug("[DefaultRetryManager:Start] starting retry manager")
	m.mutex.Lock()
	defer m.mutex.Unlock()

	if !m.running {
		m.running = true
		m.wg.Add(1)
		ticker := time.NewTicker(10 * time.Second)
		tickerChan := ticker.C

		go func() {
			for {
				select {
				case <-tickerChan:
					m.retry()
				case <-m.done:
					ticker.Stop()
					m.wg.Done()
					return
				}
			}
		}()
	}
}

func (m *DefaultRetryManager) Stop() {
	log.Debug("[DefaultRetryManager:Stop] stopping retry manager")
	m.mutex.Lock()
	defer m.mutex.Unlock()

	if m.running {
		m.done <- struct{}{}
		log.Info("retry manager: received shutdown request")
		m.wg.Wait()
		m.running = false
	}
}

func (m *DefaultRetryManager) retry() {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	for _, svc := range m.entries {
		if svc.BlockedUntil.After(time.Now()) {
			log.WithFields(log.Fields{
				"blocked_until": svc.BlockedUntil.Format(time.RFC3339),
				"action_type":   svc.ActionType,
				"service":       getKey(svc.Service),
			}).Trace("retry not permitted")
		} else {
			log.WithFields(log.Fields{
				"action_type": svc.ActionType,
				"service":     getKey(svc.Service),
			}).Debug("retrying")

			switch svc.ActionType {
			case CreateIp:
				err := m.processor.CreateIp(svc.Service)
				if err != nil {
					m.entries[getKey(svc.Service)] = RetryEntry{
						Service:      svc.Service,
						WasDeleted:   svc.WasDeleted,
						ActionType:   svc.ActionType,
						BlockedUntil: getNextRetryTime(svc.Retries),
						Retries:      svc.Retries + 1,
					}
				} else {
					log.WithFields(log.Fields{
						"action_type": svc.ActionType,
						"service":     getKey(svc.Service),
					}).Debug("retry successful")
					delete(m.entries, getKey(svc.Service))
				}
				break
			case DeleteIp:
				err := m.processor.DeleteIp(svc.Service, svc.WasDeleted)
				if err != nil {
					m.entries[getKey(svc.Service)] = RetryEntry{
						Service:      svc.Service,
						WasDeleted:   svc.WasDeleted,
						ActionType:   svc.ActionType,
						BlockedUntil: getNextRetryTime(svc.Retries),
						Retries:      svc.Retries + 1,
					}
				} else {
					log.WithFields(log.Fields{
						"action_type": svc.ActionType,
						"service":     getKey(svc.Service),
					}).Debug("retry successful")
					delete(m.entries, getKey(svc.Service))
				}
				break
			case VerifyIp:
				err := m.processor.VerifyIp(svc.Service)
				if err != nil {
					m.entries[getKey(svc.Service)] = RetryEntry{
						Service:      svc.Service,
						WasDeleted:   svc.WasDeleted,
						ActionType:   svc.ActionType,
						BlockedUntil: getNextRetryTime(svc.Retries),
						Retries:      svc.Retries + 1,
					}
				} else {
					log.WithFields(log.Fields{
						"action_type": svc.ActionType,
						"service":     getKey(svc.Service),
					}).Debug("retry successful")
					delete(m.entries, getKey(svc.Service))
				}
				break
			}
		}
	}
}

func getNextRetryTime(numberOfRetries int) time.Time {
	if numberOfRetries == 0 {
		return time.Now().Add(1 * time.Minute)
	} else if numberOfRetries == 1 {
		return time.Now().Add(2 * time.Minute)
	} else {
		return time.Now().Add(5 * time.Minute)
	}
}
