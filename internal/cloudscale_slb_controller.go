package cloudscale_slb_controller

import (
	"context"
	"fmt"
	log "github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"time"
)

const ControllerAnnotation = "linkyard.ch/slb-controller-id"

type ControllerConfig struct {
	CloudscaleToken         string
	UseFakeCloudscaleClient bool
	UseFakeNetwork          bool
	ChaosChance             float64
	ControllerId            string
	IpLimit                 int
}

func RunController(k8sClient kubernetes.Interface, config ControllerConfig, done <-chan struct{}) error {

	var cloudscaleClient CloudscaleClient
	var err error

	if config.UseFakeCloudscaleClient {
		cloudscaleClient = newFakeCloudscaleClient(config.ChaosChance)
	} else {
		cloudscaleClient, err = NewCloudscaleClient(config.CloudscaleToken, config.IpLimit)
	}
	if err != nil {
		return err
	}

	serverId, serverName, err := cloudscaleClient.GetServerId()
	if err != nil {
		return err
	}

	repository := NewRepository()
	eventProcessor := NewEventProcessor(cloudscaleClient, k8sClient, serverId, serverName, context.Background(), repository)
	retryManager := NewRetryManager(eventProcessor, repository)

	watcher := ServiceWatcher{
		processor:    eventProcessor,
		retryManager: retryManager,
		Repository:   repository,
	}

	servicesVersion, endpointsVersion, err := processServices(k8sClient, config.ControllerId, eventProcessor, retryManager, repository)
	if err != nil {
		return err
	}

	outputChannel := make(chan WatchResult)
	stopChan := make(chan struct{})
	var timer *time.Timer
	var retryTimerChan <-chan time.Time

	servicesWatch, err := k8sClient.CoreV1().Services("").Watch(metav1.ListOptions{
		Watch:           true,
		ResourceVersion: servicesVersion,
	})
	if err != nil {
		return nil
	}
	endpointsWatch, err := k8sClient.CoreV1().Endpoints("").Watch(metav1.ListOptions{
		Watch:           true,
		ResourceVersion: endpointsVersion,
	})

	retryManager.Start()
	go watcher.Watch(config.ControllerId, servicesWatch.ResultChan(), endpointsWatch.ResultChan(), stopChan, outputChannel)

	for {
		select {
		case <-done:
			log.Info("controller: received shutdown request")
			if timer != nil {
				timer.Stop()
			} else {
				stopChan <- struct{}{}
			}
			retryManager.Stop()
			return nil
		case result := <-outputChannel:
			retryManager.Stop()
			if result.Error != nil {
				log.Errorf("error while watching services (scheduling retry in 30 seconds): %v", err)
			} else {
				log.Info("watch channel was closed; re-processing services and restarting servicesWatch in 30 seconds")
			}
			timer = time.NewTimer(30 * time.Second)
			retryTimerChan = timer.C
		case <-retryTimerChan:
			timer = nil

			servicesVersion, endpointsVersion, err = processServices(k8sClient, config.ControllerId, eventProcessor, retryManager, repository)
			if err != nil {
				log.Errorf("error while processing services (scheduling retry in 30 seconds): %v", err)
				timer = time.NewTimer(30 * time.Second)
				retryTimerChan = timer.C
				continue
			}
			servicesWatch, err := k8sClient.CoreV1().Services("").Watch(metav1.ListOptions{
				Watch:           true,
				ResourceVersion: servicesVersion,
			})
			if err != nil {
				log.Errorf("error watching services (scheduling retry in 30 seconds): %v", err)
				timer = time.NewTimer(30 * time.Second)
				retryTimerChan = timer.C
				continue
			}

			endpointsWatch, err := k8sClient.CoreV1().Endpoints("").Watch(metav1.ListOptions{
				Watch:           true,
				ResourceVersion: endpointsVersion,
			})
			if err != nil {
				servicesWatch.Stop()
				log.Errorf("error watching endpoints (scheduling retry in 30 seconds): %v", err)
				timer = time.NewTimer(30 * time.Second)
				retryTimerChan = timer.C
				continue
			}

			retryManager.Start()
			go watcher.Watch(config.ControllerId, servicesWatch.ResultChan(), endpointsWatch.ResultChan(), stopChan, outputChannel)
		}
	}
}

func processServices(k8sClient kubernetes.Interface, controllerId string, eventProcessor *EventProcessor, retryManager RetryManager, repository Repository) (string, string, error) {
	repository.Clear()

	services, err := k8sClient.CoreV1().Services("").List(metav1.ListOptions{})
	if err != nil {
		return "", "", err
	}
	endpoints, err := k8sClient.CoreV1().Endpoints("").List(metav1.ListOptions{
		Limit: 1, // we're only interested in the resource version
	})
	if err != nil {
		return "", "", err
	}

	for _, svcTmp := range services.Items {
		svc := svcTmp
		if svc.Spec.Type == corev1.ServiceTypeLoadBalancer {
			annotation := svc.Annotations[ControllerAnnotation]
			if annotation != controllerId {
				log.WithFields(log.Fields{
					"svc":           fmt.Sprintf("%v/%v", svc.Namespace, svc.Name),
					"annotation":    annotation,
					"controller_id": controllerId,
				}).Info("not processing service because it is not handled by this controller")
				continue
			}

			log.WithFields(log.Fields{
				"svc": fmt.Sprintf("%v/%v", svc.Namespace, svc.Name),
			}).Debug("processing service on initial load")
			repository.PutService(svc.Namespace, svc.Name)
			ip := getServiceLbIp(&svc)

			if ip == "" {
				if !retryManager.IsAffectedByRetry(&svc, CreateIp, false) {
					err = eventProcessor.CreateIp(&svc)
					if err != nil {
						log.WithFields(log.Fields{
							"action": "CreateIp",
							"svc":    fmt.Sprintf("%v/%v", svc.Namespace, svc.Name),
							"error":  err,
						}).Warn("error while processing service; submitting for retry")
						retryManager.SubmitForUpdate(&svc, CreateIp, false)
					}
				}
			} else {
				if !retryManager.IsAffectedByRetry(&svc, VerifyIp, false) {
					err = eventProcessor.VerifyIp(&svc)
					if err != nil {
						log.WithFields(log.Fields{
							"action": "VerifyIp",
							"svc":    fmt.Sprintf("%v/%v", svc.Namespace, svc.Name),
							"error":  err,
						}).Warn("error while processing service; submitting for retry")
						retryManager.SubmitForUpdate(&svc, VerifyIp, false)
					}
				}
			}
		}
	}

	return services.ResourceVersion, endpoints.ResourceVersion, nil
}
