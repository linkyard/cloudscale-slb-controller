package cloudscale_slb_controller

import (
	"context"
	"fmt"
	"github.com/cloudscale-ch/cloudscale-go-sdk"
	log "github.com/sirupsen/logrus"
	"k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"time"
)

const ExistingIpAnnotation = "linkyard.ch/existing-floating-ip"
const PreserveIpAnnotation = "linkyard.ch/preserve-floating-ip"

type EventProcessor struct {
	cloudscaleClient CloudscaleClient
	k8sClient        kubernetes.Interface
	serverId         string
	serverName       string
	ctx              context.Context
	Repository       Repository
}

func NewEventProcessor(client CloudscaleClient, k8sClient kubernetes.Interface, serverId string, serverName string, ctx context.Context, repository Repository) *EventProcessor {
	return &EventProcessor{
		cloudscaleClient: client,
		k8sClient:        k8sClient,
		serverId:         serverId,
		serverName:       serverName,
		ctx:              ctx,
		Repository:       repository,
	}
}

func (processor *EventProcessor) CreateIp(svc *v1.Service) error {
	ip, serverName, err := processor.createIp(svc)
	if err != nil {
		log.WithFields(log.Fields{
			"svc":         getKey(svc),
			"server_name": serverName,
			"action":      "CreateIp",
			"error":       err,
		}).Error("unable to create ip")
		processor.emitEvent(
			"Warning",
			"FloatingIPCreationFailed",
			fmt.Sprintf("Failed to create floating IP: %v", err.Error()),
			"creation-failed",
			"",
			svc)
		return err
	} else {
		log.Infof("ip address %v for service %v/%v created successfully and attached to %v", ip, svc.Namespace, svc.Name, serverName)
		err = processor.updateLoadBalancerIngress(svc, ip)
		if err != nil {
			log.WithFields(log.Fields{
				"svc":         getKey(svc),
				"server_name": serverName,
				"action":      "CreateIp",
				"error":       err,
			}).Error("unable to update load balancer")
			processor.emitEvent(
				"Warning",
				"UpdateLoadBalancerFailed",
				fmt.Sprintf("Failed to update load balancer with IP %v: %v", ip, err.Error()),
				"update-lb-failed",
				ip,
				svc)
			return err
		} else {
			log.Infof("load balancer ingress for service %v/%v updated successfully and attached to %v", svc.Namespace, svc.Name, serverName)
			processor.emitEvent(
				"Normal",
				"FloatingIPCreated",
				fmt.Sprintf("FloatingIP %v successfully created on %v", ip, serverName),
				"created",
				ip,
				svc)
			return nil
		}
	}
}

func (processor *EventProcessor) DeleteIp(svc *v1.Service, wasDeleted bool) error {
	preserveIp := svc.Annotations[PreserveIpAnnotation]
	if preserveIp == "true" {
		// do not delete the ip, because the user has chosen so
		log.Infof("load balancer ip for %v/%v (%v) was not deleted, because the preserve annotation has been set", svc.Namespace, svc.Name, getServiceLbIp(svc))
		processor.emitEvent(
			"Normal",
			"FloatingIPPreserved",
			"FloatingIP preserved (put back to pool)",
			"preserved",
			getServiceLbIp(svc),
			svc)
	} else {
		// delete the ip (from cloudscale)
		err := processor.deleteIp(svc)
		if err != nil {
			log.WithFields(log.Fields{
				"svc":    getKey(svc),
				"action": "DeleteIp",
				"error":  err,
			}).Error("unable to delete ip")
			processor.emitEvent(
				"Warning",
				"FailedToDeleteFloatingIP",
				fmt.Sprintf("Failed to delete IP %v: %v", getServiceLbIp(svc), err.Error()),
				"delete-ip-failed",
				getServiceLbIp(svc),
				svc)
			return err
		} else {
			log.Infof("successfully deleted ip %v for service %v/%v", getServiceLbIp(svc), svc.Namespace, svc.Name)
			processor.emitEvent(
				"Normal",
				"FloatingIPDeleted",
				"FloatingIP deleted",
				"deleted",
				getServiceLbIp(svc),
				svc)
		}
	}
	if wasDeleted {
		//service was deleted, so we don't need to update it
		return nil
	} else {
		// remove the ip from the service
		err := processor.updateLoadBalancerIngress(svc, "")
		if err != nil {
			log.WithFields(log.Fields{
				"svc":    getKey(svc),
				"action": "DeleteIp",
				"error":  err,
			}).Error("unable to update load balancer ip")
			processor.emitEvent(
				"Warning",
				"DeleteLoadBalancerIpFailed",
				fmt.Sprintf("Failed to update load balancer IP: %v", err),
				"delete-lb-ip-failed",
				getServiceLbIp(svc),
				svc)
			return err
		}
		log.Infof("load balancer ingress for service %v/%v updated successfully", svc.Namespace, svc.Name)
		processor.emitEvent(
			"Normal",
			"FloatingIPDetached",
			"FloatingIP removed from service",
			"detached",
			getServiceLbIp(svc),
			svc)
		return nil
	}
}

func (processor *EventProcessor) ReAttach(namespace string, name string) (*v1.Service, error) {
	svc, err := processor.k8sClient.CoreV1().Services(namespace).Get(name, metav1.GetOptions{})
	if err != nil {
		log.WithFields(log.Fields{
			"svc":    getKeyFor(namespace, name),
			"action": "ReAttach",
			"error":  err,
		}).Error("unable to re-attach ip")
		processor.emitEvent(
			"Warning",
			"FloatingIPReAttachFailed",
			fmt.Sprintf("Failed to re-attach floating IP: %v", err.Error()),
			"re-attach-failed",
			"",
			svc)
		return nil, err
	}
	err = processor.VerifyIp(svc)
	return svc, err
}

func (processor *EventProcessor) VerifyIp(svc *v1.Service) error {
	ip, serverName, err := processor.verifyIp(svc)
	if err != nil {
		log.WithFields(log.Fields{
			"svc":         getKey(svc),
			"action":      "VerifyIp",
			"server_name": serverName,
			"error":       err,
		}).Error("unable to verify ip")
		processor.emitEvent(
			"Warning",
			"UnableToVerifyIp",
			fmt.Sprintf("Failed to verify IP %v: %v", ip, err.Error()),
			"verify-ip-failed",
			ip,
			svc)
		return err
	} else {
		log.Infof("successfully verified ip %v for service %v/%v attached to %v", getServiceLbIp(svc), svc.Namespace, svc.Name, serverName)
		processor.emitEvent(
			"Normal",
			"FloatingIPAttached",
			fmt.Sprintf("FloatingIP %v successfully verified on %v", ip, serverName),
			"verified",
			ip,
			svc)
		return nil
	}
}

func (processor *EventProcessor) createIp(svc *v1.Service) (string, string, error) {
	requestedIp := svc.Annotations[ExistingIpAnnotation]
	var ip FloatingIp
	var err error

	log.Debugf("[createIp] %v/%v: getting target node for new floating ip", svc.Namespace, svc.Name)
	serverId, serverName, err := processor.getFloatingIpTargetServerForNewIp(svc)
	if err != nil {
		return "", serverId, err
	}

	if requestedIp != "" {
		ctx, _ := context.WithTimeout(processor.ctx, 60*time.Second)
		ip, err = processor.cloudscaleClient.GetFloatingIp(ctx, requestedIp)
	} else {
		ctx, _ := context.WithTimeout(processor.ctx, 300*time.Second)
		ip, err = processor.cloudscaleClient.CreateFloatingIp(ctx, serverId)
	}

	if err != nil {
		return "", serverName, err
	}
	processor.Repository.PutFloatingIp(svc.Namespace, svc.Name, ip.Ip, serverName)
	return ip.Ip, serverName, err
}

func (processor *EventProcessor) deleteIp(svc *v1.Service) error {
	serviceIp := getServiceLbIp(svc)
	log.Debugf("[deleteIp] %v/%v: getting floating ip %v", svc.Namespace, svc.Name, serviceIp)
	ctx, _ := context.WithTimeout(processor.ctx, 60*time.Second)
	_, err := processor.cloudscaleClient.GetFloatingIp(ctx, serviceIp)
	if err != nil {
		errorResponse, ok := err.(*cloudscale.ErrorResponse)
		if !ok || (ok && errorResponse.StatusCode != 404) {
			return err
		}
	}
	log.Debugf("[deleteIp] %v/%v: deleting floating ip %v", svc.Namespace, svc.Name, serviceIp)
	ctx, _ = context.WithTimeout(processor.ctx, 300*time.Second)
	err = processor.cloudscaleClient.DeleteFloatingIp(ctx, serviceIp)
	if err != nil {
		processor.Repository.DeleteFloatingIp(serviceIp)
	}
	return err
}

func (processor *EventProcessor) verifyIp(svc *v1.Service) (string, string, error) {
	ctx, _ := context.WithTimeout(processor.ctx, 60*time.Second)
	ip, err := processor.cloudscaleClient.GetFloatingIp(ctx, getServiceLbIp(svc))
	if err != nil {
		errorResponse, ok := err.(*cloudscale.ErrorResponse)
		if !ok || (ok && errorResponse.StatusCode != 404) {
			return "", "", err
		}
	}
	if ip.Ip == "" {
		ip, serverName, err := processor.createIp(svc)
		if err != nil {
			return "", "", err
		}
		processor.emitEvent(
			"Normal",
			"FloatingIPCreated",
			fmt.Sprintf("FloatingIP %v successfully created on %v", ip, serverName),
			"created",
			ip,
			svc)
		return ip, serverName, nil
	}

	endpoints, err := processor.k8sClient.CoreV1().Endpoints(svc.Namespace).Get(svc.Name, metav1.GetOptions{})
	if err != nil {
		return ip.Ip, "", err
	}

	// check if the floating ip is attached to a node where one of the pods is running on
	log.Debugf("[verifyIp] %v/%v: checking if floating ip is attached to a node with a running pod", svc.Namespace, svc.Name)
	ctx, _ = context.WithTimeout(processor.ctx, 60*time.Second)
	serverName, err := processor.cloudscaleClient.GetServerNameForServerId(ctx, ip.ServerId)
	if err != nil {
		return ip.Ip, serverName, err
	}

	log.Debugf("[verifyIp] %v/%v: floating ip %v is attached to node %v", svc.Namespace, svc.Name, ip.Ip, serverName)

	firstReadyNode := ""
	for _, subset := range endpoints.Subsets {
		for _, address := range subset.Addresses {
			tmpAddress := address
			if firstReadyNode == "" {
				firstReadyNode = *tmpAddress.NodeName
				log.Debugf("[verifyIp] %v/%v: first node with ready pod is %v", svc.Namespace, svc.Name, firstReadyNode)
			}
			if serverName == *tmpAddress.NodeName {
				log.Debugf("[verifyIp] %v/%v: floating ip %v is attached to server %v which is running a ready pod", svc.Namespace, svc.Name, ip.Ip, serverName)
				processor.Repository.PutFloatingIp(svc.Namespace, svc.Name, ip.Ip, serverName)
				return ip.Ip, serverName, err
			}
		}
	}

	log.Debugf("[verifyIp] %v/%v: floating ip %v not attached to a node with a running ready pod", svc.Namespace, svc.Name, ip.Ip)
	if firstReadyNode != "" {
		log.Debugf("[verifyIp] %v/%v: looking up server id for node %v", svc.Namespace, svc.Name, firstReadyNode)
		ctx, _ = context.WithTimeout(processor.ctx, 60*time.Second)
		serverId, err := processor.cloudscaleClient.GetServerIdForNode(ctx, firstReadyNode)
		if err != nil {
			return ip.Ip, firstReadyNode, err
		}
		log.Debugf("[verifyIp] %v/%v: re-attaching floating ip %v to %v", svc.Namespace, svc.Name, ip.Ip, firstReadyNode)
		floatingIp, err := processor.cloudscaleClient.UpdateFloatingIp(ctx, ip.Ip, serverId)
		if err != nil {
			return ip.Ip, firstReadyNode, err
		}
		processor.Repository.PutFloatingIp(svc.Namespace, svc.Name, floatingIp.Ip, firstReadyNode)
		return ip.Ip, firstReadyNode, err
	} else {
		log.Debugf("[verifyIp] %v/%v: no ready node found for floating ip %v", svc.Namespace, svc.Name, ip.Ip)
	}

	return ip.Ip, serverName, err
}

func (processor *EventProcessor) updateLoadBalancerIngress(svc *v1.Service, ip string) error {
	if ip == "" {
		svc.Status.LoadBalancer.Ingress = make([]v1.LoadBalancerIngress, 0)
	} else {
		svc.Status.LoadBalancer.Ingress = []v1.LoadBalancerIngress{{IP: ip}}
	}
	_, e := processor.k8sClient.CoreV1().Services(svc.Namespace).UpdateStatus(svc)
	return e
}

// returns the server id on which a new floating ip should be attached to
func (processor *EventProcessor) getFloatingIpTargetServerForNewIp(svc *v1.Service) (string, string, error) {
	log.Debugf("[getFloatingIpTargetServerForNewIp] %v/%v: fetching endpoints", svc.Namespace, svc.Name)
	endpoints, err := processor.k8sClient.CoreV1().Endpoints(svc.Namespace).Get(svc.Name, metav1.GetOptions{})
	if err != nil {
		statusErr, ok := err.(*errors.StatusError)
		if ok && statusErr.Status().Reason == metav1.StatusReasonNotFound {
			// use the server where this process is running on
			log.Debugf("[getFloatingIpTargetServerForNewIp] %v/%v: no endpoints object found; using %v", svc.Namespace, svc.Name, processor.serverName)
			return processor.serverId, processor.serverName, nil
		}

		return "", "", err
	}
	// use the first one that is ready
	for _, subset := range endpoints.Subsets {
		for _, address := range subset.Addresses {
			log.Debugf("[getFloatingIpTargetServerForNewIp] %v/%v: found node %v with ready pod", svc.Namespace, svc.Name, *address.NodeName)
			return processor.getServerId(*address.NodeName)
		}
	}
	// use the first one that is not (yet) ready
	for _, subset := range endpoints.Subsets {
		for _, address := range subset.NotReadyAddresses {
			log.Debugf("[getFloatingIpTargetServerForNewIp] %v/%v: found node %v with NOT ready pod", svc.Namespace, svc.Name, *address.NodeName)
			return processor.getServerId(*address.NodeName)
		}
	}
	// use the server where this process is running on
	log.Debugf("[getFloatingIpTargetServerForNewIp] %v/%v: no ready or not ready nodes found; using %v", svc.Namespace, svc.Name, processor.serverName)
	return processor.serverId, processor.serverName, nil
}

func (processor *EventProcessor) getServerId(nodeName string) (string, string, error) {
	ctx, _ := context.WithTimeout(context.Background(), 60*time.Second)
	serverIdForNode, err := processor.cloudscaleClient.GetServerIdForNode(ctx, nodeName)
	return serverIdForNode, nodeName, err
}

func (processor *EventProcessor) emitEvent(eventType string, reason string, message string, action string, ip string, svc *v1.Service) {
	event, err := processor.k8sClient.CoreV1().Events(svc.Namespace).Get(fmt.Sprintf("%v.%v-%v-%v", svc.Name, "slb", action, ip),
		metav1.GetOptions{})
	if err == nil {
		event.Count = event.Count + 1
		event.LastTimestamp = metav1.NewTime(time.Now())
		event.InvolvedObject = v1.ObjectReference{
			Kind:            "Service",
			Namespace:       svc.Namespace,
			Name:            svc.Name,
			APIVersion:      "v1",
			ResourceVersion: svc.ResourceVersion,
			UID:             svc.UID,
		}

		_, err := processor.k8sClient.CoreV1().Events(svc.Namespace).Update(event)
		if err != nil {
			log.WithFields(log.Fields{
				"event_reason":  reason,
				"event_message": message,
				"error":         err,
			}).Warn("unable to update event")
		}
	} else {
		_, err := processor.k8sClient.CoreV1().Events(svc.Namespace).Create(&v1.Event{
			ObjectMeta: metav1.ObjectMeta{
				Name:              fmt.Sprintf("%v.%v-%v-%v", svc.Name, "slb", action, ip),
				CreationTimestamp: metav1.NewTime(time.Now()),
				Namespace:         svc.Namespace,
			},
			InvolvedObject: v1.ObjectReference{
				Kind:            "Service",
				Namespace:       svc.Namespace,
				Name:            svc.Name,
				APIVersion:      "v1",
				ResourceVersion: svc.ResourceVersion,
				UID:             svc.UID,
			},
			Reason:         reason,  // why the action was taken
			Message:        message, // human readable
			Type:           eventType,
			FirstTimestamp: metav1.NewTime(time.Now()),
			LastTimestamp:  metav1.NewTime(time.Now()),
			Count:          1,
			Source: v1.EventSource{
				Component: "cloudscale-slb-controller",
			},
		})
		if err != nil {
			log.WithFields(log.Fields{
				"event_reason":  reason,
				"event_message": message,
				"error":         err,
			}).Warn("unable to emit event")
		}
	}

}
