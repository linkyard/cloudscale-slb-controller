package cloudscale_slb_controller

import (
	log "github.com/sirupsen/logrus"
	"k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
	"k8s.io/client-go/tools/record"
	"time"
)

type ElectionConfig struct {
	Hostname           string
	ConfigMapNamespace string
	ConfigMapName      string
	TTL                time.Duration
	ElectionId         string
	Callbacks          leaderelection.LeaderCallbacks
}

func CreateNewElection(k8sClient kubernetes.Interface, config ElectionConfig) (*leaderelection.LeaderElector, error) {
	log.Debugf("[leaderElection:startElection]")

	broadcaster := record.NewBroadcaster()
	recorder := broadcaster.NewRecorder(scheme.Scheme,
		v1.EventSource{
			Component: "cloudscale-slb-controller-leader-elector",
			Host:      config.Hostname,
		})

	lock := &resourcelock.ConfigMapLock{
		Client: k8sClient.CoreV1(),
		ConfigMapMeta: metav1.ObjectMeta{
			Namespace: config.ConfigMapNamespace,
			Name:      config.ConfigMapName,
		},
		LockConfig: resourcelock.ResourceLockConfig{
			Identity:      config.ElectionId,
			EventRecorder: recorder,
		},
	}
	leaderElectionConfig := leaderelection.LeaderElectionConfig{
		Callbacks:     config.Callbacks,
		Lock:          lock,
		LeaseDuration: config.TTL,
		RenewDeadline: config.TTL / 2,
		RetryPeriod:   config.TTL / 4,
	}

	return leaderelection.NewLeaderElector(leaderElectionConfig)
}
