package main

import (
	"context"
	"fmt"
	"github.com/linkyard/cloudscale-slb-controller/internal"
	"github.com/namsral/flag"
	log "github.com/sirupsen/logrus"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/leaderelection"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
)

func main() {

	for _, arg := range os.Args {
		if strings.Index(arg, "help") != -1 || strings.Index(arg, "-h") != -1 || strings.Index(arg, "--help") != -1 {
			fmt.Println(`Usage:
cloudscale_slb_controller [flags]

Flags:
-cloudscale-token           cloudscale.ch API token [CLOUDSCALE_TOKEN]

-ip-limit					maximum number of floating ips that are allowed to exist on the
                            cloudscale.ch account (default: 5); used to make sure we don't go
                            bankrupt if this component runs amok [IP_LIMIT]

-kubeconfig                 path to a kubernetes kubeconfig [KUBECONFIG]

-controller-id				id of this controller; if set, the controller will only process services with a
                            linkyard.ch/slb-controller-id annotation set to this value [CONTROLLER_ID]

-leader-election-configmap  name of the configmap used for leader election [LEADER_ELECTION_CONFIGMAP]

-leader-election-namespace  namespace of the configmap used for leader election [LEADER_ELECTION_NAMESPACE]

-leader-election-node		name of this node for leader-election purposes [LEADER_ELECTION_NODE]

-leader-election-ttl	    TTL for leader election, e.g. 10s; valid time units are "ns", "us" (or "µs"),
						    "ms", "s", "m", "h" defaults to "10s" [LEADER_ELECTION_TTL]

-fake-cloudscale-client     set to true to use a fake cloudscale client [FAKE_CLOUDSCALE_CLIENT]

-chaos-chance               chance of one of the fake components failing; range [0.0,1.0) [CHAOS_CHANCE]

-log-level					logging level to set (default: Debug): Panic, Fatal, Error, Warn, Info, Debug, Trace [LOG_LEVEL]`)
			os.Exit(1)
		}
	}

	var cloudscaleToken string
	var ipLimit int
	var kubeconfig string
	var controllerId string
	var leaderElectionConfigmap string
	var leaderElectionNamespace string
	var leaderElectionNode string
	var leaderElectionTTL string

	var useFakeCloudscaleClient bool
	var chaosChance float64
	var logLevel string

	flag.StringVar(&cloudscaleToken, "cloudscale-token", "", "cloudscale.ch API token")
	flag.IntVar(&ipLimit, "ip-limit", 5, "floating ip limit on the cloudscale.ch account")
	flag.StringVar(&kubeconfig, "kubeconfig", "", "kubernetes kubeconfig")
	flag.StringVar(&controllerId, "controller-id", "", "controller-id")
	flag.BoolVar(&useFakeCloudscaleClient, "fake-cloudscale-client", false, "use a fake cloudscale client")
	flag.Float64Var(&chaosChance, "chaos-chance", 0, "chance of one of the fake components failing")
	flag.StringVar(&logLevel, "log-level", "Debug", "logging level to set")
	flag.StringVar(&leaderElectionConfigmap, "leader-election-configmap", "", "configmap to use for leader election")
	flag.StringVar(&leaderElectionNamespace, "leader-election-namespace", "", "namespace to use for leader election")
	flag.StringVar(&leaderElectionNode, "leader-election-node", "", "name of this node for leader-election")
	flag.StringVar(&leaderElectionTTL, "leader-election-ttl", "10s", "TTL for leader election")

	flag.Parse()

	logLevel = strings.ToLower(logLevel)
	if logLevel == "panic" {
		log.SetLevel(log.PanicLevel)
	} else if logLevel == "fatal" {
		log.SetLevel(log.FatalLevel)
	} else if logLevel == "error" {
		log.SetLevel(log.ErrorLevel)
	} else if logLevel == "warn" {
		log.SetLevel(log.WarnLevel)
	} else if logLevel == "info" {
		log.SetLevel(log.InfoLevel)
	} else if logLevel == "debug" {
		log.SetLevel(log.DebugLevel)
	} else if logLevel == "trace" {
		log.SetLevel(log.TraceLevel)
	} else {
		log.SetLevel(log.DebugLevel)
	}

	if cloudscaleToken == "" && !useFakeCloudscaleClient {
		log.Error("flag --cloudscale-token is unset")
		os.Exit(1)
	}
	if leaderElectionConfigmap == "" {
		log.Error("flag --leader-election-configmap is unset")
		os.Exit(1)
	}
	if leaderElectionNamespace == "" {
		log.Error("flag --leader-election-namespace is unset")
		os.Exit(1)
	}
	if leaderElectionNode == "" {
		log.Error("flag --leader-election-node is unset")
		os.Exit(1)
	}
	leaderElectionTTLDuration, parseErr := time.ParseDuration(leaderElectionTTL)
	if parseErr != nil {
		log.Errorf("invalid value '%v' for leader election ttl: %v", leaderElectionTTL, parseErr.Error())
		os.Exit(1)
	}

	var config *rest.Config
	var err error
	if kubeconfig != "" {
		config, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
		if err != nil {
			log.Errorf("cannot use kubeconfig to initialize client: %s", err.Error())
			os.Exit(1)
		}
	} else {
		config, err = rest.InClusterConfig()
		if err != nil {
			log.Errorf("cannot use in-cluster config to initialize client: %s", err.Error())
			os.Exit(1)
		}
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		log.Errorf("unable to initialize kubernetes client: %s", err.Error())
		os.Exit(1)
	}

	controllerConfig := cloudscale_slb_controller.ControllerConfig{
		CloudscaleToken:         cloudscaleToken,
		UseFakeCloudscaleClient: useFakeCloudscaleClient,
		ChaosChance:             chaosChance,
		ControllerId:            controllerId,
		IpLimit:                 ipLimit,
	}

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	doneChan := make(chan struct{})
	electionContext, cancelElection := context.WithCancel(context.Background())

	go func() {
		sig := <-sigs
		doneChan <- struct{}{}
		cancelElection()
		log.Infof("Received signal %v; shutting down", sig)
	}()

	var wg sync.WaitGroup
	wg.Add(1)

	callbacks := leaderelection.LeaderCallbacks{
		OnNewLeader: func(identity string) {
			log.Infof("current leader: %v", identity)
		},
		OnStoppedLeading: func() {
			log.Infof("leader lock lost; stopping controller (%v)", leaderElectionNode)
			doneChan <- struct{}{}
		},
		OnStartedLeading: func(context context.Context) {
			log.Infof("leader lock acquired; running controller (%v)", leaderElectionNode)
			err = cloudscale_slb_controller.RunController(clientset, controllerConfig, doneChan)
			if err != nil {
				log.Errorf("unable to run service watcher: %v", err)

			} else {
				log.Info("service watcher terminated")
			}
			wg.Done()
		},
	}

	election, err := cloudscale_slb_controller.CreateNewElection(clientset, cloudscale_slb_controller.ElectionConfig{
		Hostname:           leaderElectionNode,
		ConfigMapNamespace: leaderElectionNamespace,
		ConfigMapName:      leaderElectionConfigmap,
		TTL:                leaderElectionTTLDuration,
		ElectionId:         leaderElectionNode,
		Callbacks:          callbacks,
	})

	go election.Run(electionContext)

	wg.Wait()
}
