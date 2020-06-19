package cloudscale_slb_controller

import (
	"fmt"
	log "github.com/sirupsen/logrus"
	"k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/watch"
)

type ServiceWatcher struct {
	processor    *EventProcessor
	retryManager RetryManager
	Repository   Repository
}

type WatchResult struct {
	ResourceVersion string
	Error           error
}

func (s ServiceWatcher) Watch(controllerId string,
	servicesChannel <-chan watch.Event, endpointsChannel <-chan watch.Event,
	done <-chan struct{}, outputChannel chan WatchResult) {

	lastResourceVersion := ""

	for {
		select {
		case event, ok := <-endpointsChannel:
			// channel is closed
			if !ok {
				log.Debugf("Watch channel for endpoints is closed")
				outputChannel <- WatchResult{ResourceVersion: lastResourceVersion}
				return
			}

			endpoints := getEndpoints(event)
			lastResourceVersion = endpoints.ObjectMeta.GetResourceVersion()

			if endpoints != nil {
				if s.Repository.GetService(endpoints.Namespace, endpoints.Name) && (event.Type == watch.Modified || event.Type == watch.Added) {
					fipInfo, ok := s.Repository.GetFloatingIpForService(endpoints.Namespace, endpoints.Name)
					if !ok {
						log.Debugf("ignoring change %v for endpoints %v/%v because there is no floating ip for the service", event.Type, endpoints.Namespace, endpoints.Name)
					} else {
						attached := isFipAttachedToReadyAddress(fipInfo, endpoints)
						if !attached {
							if !s.retryManager.IsEndpointsAffectedByRetry(endpoints.Namespace, endpoints.Name) {
								svc, err := s.processor.ReAttach(endpoints.Namespace, endpoints.Name)
								if err != nil {
									if svc != nil {
										s.retryManager.SubmitForUpdate(svc, VerifyIp, false)
									}
								}
							}
						} else {
							log.Debugf("ignoring change %v for endpoints %v/%v because floating ip is already properly attached", event.Type, endpoints.Namespace, endpoints.Name)
						}
					}
				} else {
					log.Tracef("ignoring irrelevant change %v for endpoints %v/%v", event.Type, endpoints.Namespace, endpoints.Name)
				}
			}

		case event, ok := <-servicesChannel:
			// channel is closed
			if !ok {
				log.Debugf("Watch channel for services is closed")
				outputChannel <- WatchResult{ResourceVersion: lastResourceVersion}
				return
			}

			action := getAction(event, controllerId)
			lastResourceVersion = action.Service.ObjectMeta.GetResourceVersion()

			switch action.ActionType {
			case CreateIp:
				if !s.retryManager.IsAffectedByRetry(action.Service, CreateIp, false) {
					err := s.processor.CreateIp(action.Service)
					if err != nil {
						s.retryManager.SubmitForUpdate(action.Service, CreateIp, false)
					}
				}
				break
			case DeleteIp:
				if !s.retryManager.IsAffectedByRetry(action.Service, DeleteIp, event.Type == watch.Deleted) {
					err := s.processor.DeleteIp(action.Service, event.Type == watch.Deleted)
					if err != nil {
						s.retryManager.SubmitForUpdate(action.Service, DeleteIp, event.Type == watch.Deleted)
					}
				}
				break
			case VerifyIp:
				if !s.retryManager.IsAffectedByRetry(action.Service, VerifyIp, false) {
					err := s.processor.VerifyIp(action.Service)
					if err != nil {
						s.retryManager.SubmitForUpdate(action.Service, VerifyIp, false)
					}
				}
				break
			case Ignore:
				s.retryManager.IsAffectedByRetry(action.Service, Ignore, false)
			default:
				// do nothing
			}
		case <-done:
			log.Debugf("received message from done channel")
			outputChannel <- WatchResult{ResourceVersion: lastResourceVersion}
			return
		}
	}

}

type Action struct {
	ActionType ActionType
	Service    *v1.Service
}

type ActionType string

const (
	CreateIp ActionType = "CreateIp"
	DeleteIp ActionType = "DeleteIp"
	Ignore   ActionType = "Ignore"
	VerifyIp ActionType = "VerifyIp"
)

func getAction(event watch.Event, controllerId string) Action {
	svc := getService(event)
	annotation := svc.Annotations[ControllerAnnotation]

	if svc == nil {
		log.WithFields(log.Fields{
			"event_type":   event.Type,
			"event_object": event.Object,
		}).Warn("ignoring event for an object that is not a service")
		return Action{ActionType: Ignore, Service: nil}
	}

	if annotation != controllerId {
		log.WithFields(log.Fields{
			"event_type":    event.Type,
			"annotation":    annotation,
			"service":       fmt.Sprintf("%s/%s", svc.Namespace, svc.Name),
			"controller_id": controllerId,
		}).Info("not processing service, because it is not handled by this controller")
		return Action{ActionType: Ignore, Service: svc}
	}

	if svc.Spec.Type != v1.ServiceTypeLoadBalancer {
		ip := getServiceLbIp(svc)
		if ip == "" {
			log.WithFields(log.Fields{
				"event_type":   event.Type,
				"service":      fmt.Sprintf("%s/%s", svc.Namespace, svc.Name),
				"service_type": svc.Spec.Type,
			}).Trace("ignoring service because it is not of type LoadBalancer")
			return Action{ActionType: Ignore, Service: svc}
		} else {
			log.WithFields(log.Fields{
				"event_type":   event.Type,
				"service":      fmt.Sprintf("%s/%s", svc.Namespace, svc.Name),
				"service_type": svc.Spec.Type,
				"ip":           ip,
			}).Debug("service is not of type LoadBalancer but it has an ip; submitting ip for deletion")
			return Action{ActionType: DeleteIp, Service: svc}
		}
	}

	switch event.Type {

	case watch.Added:
		serviceIp := getServiceLbIp(svc)
		if serviceIp == "" {
			log.WithFields(log.Fields{
				"event_type": event.Type,
				"service":    fmt.Sprintf("%s/%s", svc.Namespace, svc.Name),
				"service_ip": serviceIp,
			}).Debug("event implies that an ip needs to be created")
			return Action{ActionType: CreateIp, Service: svc}
		}

		log.WithFields(log.Fields{
			"event_type": event.Type,
			"service":    fmt.Sprintf("%s/%s", svc.Namespace, svc.Name),
			"service_ip": serviceIp,
		}).Debug("event implies that the service already has an ip")
		return Action{ActionType: VerifyIp, Service: svc}

	case watch.Modified:
		serviceIp := getServiceLbIp(svc)
		if serviceIp == "" {
			log.WithFields(log.Fields{
				"event_type": event.Type,
				"service":    fmt.Sprintf("%s/%s", svc.Namespace, svc.Name),
				"service_ip": "",
			}).Debug("event implies that an ip needs to be created")
			return Action{ActionType: CreateIp, Service: svc}
		}

		log.WithFields(log.Fields{
			"event_type": event.Type,
			"service":    fmt.Sprintf("%s/%s", svc.Namespace, svc.Name),
			"service_ip": serviceIp,
		}).Debug("event implies that the service already has an ip")
		return Action{ActionType: VerifyIp, Service: svc}

	case watch.Deleted:
		serviceIp := getServiceLbIp(svc)
		if serviceIp == "" {
			log.WithFields(log.Fields{
				"event_type": event.Type,
				"service":    fmt.Sprintf("%s/%s", svc.Namespace, svc.Name),
				"service_ip": serviceIp,
			}).Debug("ignoring event because service does not have a load balancer ip")
			return Action{ActionType: Ignore, Service: svc}
		}

		log.WithFields(log.Fields{
			"event_type": event.Type,
			"service":    fmt.Sprintf("%s/%s", svc.Namespace, svc.Name),
			"service_ip": serviceIp,
		}).Debug("event implies that the ip needs to be deleted")
		return Action{ActionType: DeleteIp, Service: svc}

	default:
		log.WithFields(log.Fields{
			"event_type": event.Type,
			"service":    fmt.Sprintf("%s/%s", svc.Namespace, svc.Name),
		}).Debug("ignoring event because it is not relevant")
		return Action{ActionType: Ignore, Service: svc}

	}
}

func isFipAttachedToReadyAddress(fipInfo FloatingIpInformation, endpoints *v1.Endpoints) bool {
	for _, subset := range endpoints.Subsets {
		for _, address := range subset.Addresses {
			tmpAddress := address
			if *tmpAddress.NodeName == fipInfo.ServiceName {
				return true
			}
		}
	}
	return false
}

func getService(event watch.Event) *v1.Service {
	if svc, ok := event.Object.(*v1.Service); ok {
		return svc
	}
	return nil
}

func getEndpoints(event watch.Event) *v1.Endpoints {
	if endpoints, ok := event.Object.(*v1.Endpoints); ok {
		return endpoints
	}
	return nil
}

func getServiceLbIp(svc *v1.Service) string {
	ingresses := svc.Status.LoadBalancer.Ingress
	if len(ingresses) == 0 {
		return ""
	}
	return ingresses[0].IP
}
