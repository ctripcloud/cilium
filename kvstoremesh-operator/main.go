// SPDX-License-Identifier: Apache-2.0
// Copyright 2022 Authors of Cilium

package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/cilium/cilium/kvstoremesh-operator/defaults"
	"github.com/cilium/cilium/kvstoremesh-operator/metrics"
	"github.com/cilium/cilium/kvstoremesh-operator/option"
	"github.com/cilium/cilium/pkg/k8s"
	"github.com/cilium/cilium/pkg/kvstoremesh"
	"github.com/cilium/cilium/pkg/logging"
	"github.com/cilium/cilium/pkg/logging/logfields"
	ciliumOption "github.com/cilium/cilium/pkg/option"
	"github.com/cilium/cilium/pkg/pprof"
	"github.com/cilium/cilium/pkg/rand"

	k8sversion "github.com/cilium/cilium/pkg/k8s/version"
	gops "github.com/google/gops/agent"
	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"golang.org/x/sys/unix"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
)

var (
	// Leader election resource lock name
	resourceLockName = "kvstoremesh-operator-resource-lock"
	binaryName       = filepath.Base(os.Args[0])
	log              = logging.DefaultLogger.WithField(logfields.LogSubsys, binaryName)

	rootCmd = &cobra.Command{
		Use:   binaryName,
		Short: "Run " + binaryName,
		Run: func(cmd *cobra.Command, args []string) {

			// Open socket for using gops to get stacktraces of the agent.
			addr := fmt.Sprintf("127.0.0.1:%d", viper.GetInt(option.GopsPort))
			addrField := logrus.Fields{"address": addr}
			if err := gops.Listen(gops.Options{
				Addr:                   addr,
				ReuseSocketAddrAndPort: true,
			}); err != nil {
				log.WithError(err).WithFields(addrField).Fatal("Cannot start gops server")
			}
			log.WithFields(addrField).Info("Started gops server")

			fmt.Println(` _  ____     ______  _                 __  __           _     `)
			fmt.Println(`| |/ /\ \   / / ___|| |_ ___  _ __ ___|  \/  | ___  ___| |__  `)
			fmt.Println(`|   /  \ \ / /\___ \| __/ _ \|  __/ _ \ |\/| |/ _ \/ __|  _ \ `)
			fmt.Println(`|   \   \ V /  ___) | || (_) | | |  __/ |  | |  __/\__ \ | | |`)
			fmt.Println(`|_|\_\   \_/  |____/ \__\___/|_|  \___|_|  |_|\___||___/_| |_|`)
			log.Infof("Cilium KVStoreMesh %s", option.Version)

			// Parse CLI parameters
			option.Config.Populate()

			// Init logging
			if err := logging.SetupLogging(option.Config.LogDriver,
				logging.LogOptions(option.Config.LogOpt), binaryName, option.Config.Debug); err != nil {
				log.Fatal(err)
			}

			// Validate final configurations
			if err := option.Config.Validate(); err != nil {
				log.WithError(err).Fatal("invalid configuration")
			}

			// Print effective configurations that will be used
			ciliumOption.LogRegisteredOptions(log)

			if option.Config.PProf {
				pprof.Enable(option.Config.PProfPort)
			}

			// Enable fallback to direct API probing to check for support of Leases in
			// case Discovery API fails.
			ciliumOption.Config.EnableK8sLeasesFallbackDiscovery()

			// Start KVStoreMesh operator
			startKVStoreMeshOperator()
		},
	}

	shutdownSignal = make(chan struct{})

	// Use a Go context so we can tell the leaderelection code when we
	// want to step down
	leaderElectionCtx, leaderElectionCtxCancel = context.WithCancel(context.Background())

	// isLeader is an atomic boolean value that is true when the operator is
	// elected leader. Otherwise, it is false.
	isLeader atomic.Value
)

func init() {
	cobra.OnInitialize(option.InitConfig(binaryName))

	flags := rootCmd.Flags()

	flags.String(option.ConfigDir, "", `Configuration directory that contains a file for each option`)
	ciliumOption.BindEnv(option.ConfigDir)

	flags.String(option.ClusterName, defaults.ClusterName, "Name of the cluster")
	ciliumOption.BindEnv(option.ClusterName)

	flags.Int(option.ClusterIDName, 0, "Unique identifier of the cluster")
	ciliumOption.BindEnv(option.ClusterIDName)

	// K8s options
	flags.String(option.K8sKubeConfigPath, "", "K8s kubeconfig file")
	ciliumOption.BindEnv(option.K8sKubeConfigPath)

	flags.Duration(option.K8sSyncTimeoutName, defaults.K8sSyncTimeout, "Timeout for synchronizing k8s resources before exiting")
	ciliumOption.BindEnv(option.K8sSyncTimeoutName)

	// Leader election options
	flags.Duration(option.LeaderElectionLeaseDuration, 15*time.Second,
		"Duration that non-leader operator candidates will wait before forcing to acquire leadership")
	ciliumOption.BindEnv(option.LeaderElectionLeaseDuration)

	flags.Duration(option.LeaderElectionRenewDeadline, 10*time.Second,
		"Duration that current acting master will retry refreshing leadership in before giving up the lock")
	ciliumOption.BindEnv(option.LeaderElectionRenewDeadline)

	flags.Duration(option.LeaderElectionRetryPeriod, 2*time.Second,
		"Duration that LeaderElector clients should wait between retries of the actions")
	ciliumOption.BindEnv(option.LeaderElectionRetryPeriod)

	// KVstore options
	flags.String(option.KVStore, option.KVStoreTypeEtcd, "Key-value store type. e.g. etcd")
	ciliumOption.BindEnv(option.KVStore)

	flags.Int(option.KVstoreMaxConsecutiveQuorumErrorsName, defaults.KVstoreMaxConsecutiveQuorumErrors, "Max acceptable kvstore consecutive quorum errors before the agent assumes permanent failure")
	ciliumOption.BindEnv(option.KVstoreMaxConsecutiveQuorumErrorsName)

	flags.Var(ciliumOption.NewNamedMapOptions(option.KVStoreOpt, &option.Config.KVStoreOpt, nil),
		option.KVStoreOpt, "Options for the local key-value store")
	ciliumOption.BindEnv(option.KVStoreOpt)

	flags.String(option.RemoteKVStoresConfigDirName, "", "Directory containing remote kvstores' configuration files")
	ciliumOption.BindEnv(option.RemoteKVStoresConfigDirName)

	flags.Duration(option.KVstoreLeaseTTL, defaults.KVstoreLeaseTTL, "Time-to-live for the KVstore lease.")
	ciliumOption.BindEnv(option.KVstoreLeaseTTL)

	// API options
	flags.String(option.APIServeAddr, defaults.APIServeAddr, "Address to serve API requests")
	ciliumOption.BindEnv(option.APIServeAddr)

	flags.String(option.PrometheusServeAddr, defaults.PrometheusServeAddr, "Address to serve Prometheus metrics")
	ciliumOption.BindEnv(option.PrometheusServeAddr)

	// Debug options
	flags.BoolP(option.DebugArg, "D", false, "Enable debugging mode")
	ciliumOption.BindEnv(option.DebugArg)

	flags.Int(option.GopsPort, defaults.GopsPort, "Port for gops server to listen on")
	ciliumOption.BindEnv(option.GopsPort)

	flags.StringSlice(option.LogDriver, []string{}, "Logging endpoints to use, for example: syslog")
	ciliumOption.BindEnv(option.LogDriver)

	flags.Bool(option.Version, false, "Print version information")
	ciliumOption.BindEnv(option.Version)

	flags.Bool(option.PProf, false, "Enable serving the pprof debugging API")
	ciliumOption.BindEnv(option.PProf)

	flags.Int(option.PProfPort, 6061, "Port that the pprof listens on")
	ciliumOption.BindEnv(option.PProfPort)

	viper.BindPFlags(flags)
}

func main() {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, unix.SIGINT, unix.SIGTERM)

	go func() {
		<-signals
		doCleanup()
	}()

	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(-1)
	}
}

func startKVStoreMeshOperator() {
	log.WithFields(logrus.Fields{
		"clusterName":   option.Config.ClusterName,
		"clusterID":     option.Config.ClusterID,
		"identityStart": option.Config.IdentityStart,
		"identityEnd":   option.Config.IdentityEnd,
	}).Info("Local cluster information")

	metrics.Register()

	k8sInitDone := make(chan struct{})
	isLeader.Store(false)

	go startServer(shutdownSignal, k8sInitDone, option.Config.APIServeAddr)

	// Init k8s subsystem
	log.Info("Initializing K8s subsystem")
	initK8s(k8sInitDone)

	capabilities := k8sversion.Capabilities()
	if !capabilities.MinimalVersionMet {
		log.Fatalf("Minimal kubernetes version not met: %s < %s",
			k8sversion.Version(), k8sversion.MinimalVersionConstraint)
	}

	// Support HA mode only for Kubernetes Versions having support for
	// LeasesResourceLock.
	// See docs on capabilities.LeasesResourceLock for more context.
	if !capabilities.LeasesResourceLock {
		log.Info("Support for coordination.k8s.io/v1 not present, fallback to non HA mode")
		onStartLeading(leaderElectionCtx)
		return
	}

	// Get hostname for identity name of the lease lock holder.
	// We identify the leader of the operator cluster using hostname.
	operatorID, err := os.Hostname()
	if err != nil {
		log.WithError(err).Fatal("Failed to get hostname when generating lease lock identity")
	}

	operatorID = rand.RandomStringWithPrefix(operatorID+"-", 10)

	// Hardcode this setting currently.
	// cilium-operator sets this parameter in it's docker image ENV: CILIUM_K8S_NAMESPACE=kube-system
	ciliumOption.Config.K8sNamespace = "kube-system"
	ns := ciliumOption.Config.K8sNamespace
	// If due to any reason the CILIUM_K8S_NAMESPACE is not set we assume the operator
	// to be in default namespace.
	if ns == "" {
		ns = metav1.NamespaceDefault
	}

	resourceLock := &resourcelock.LeaseLock{
		LeaseMeta: metav1.ObjectMeta{
			Name:      resourceLockName,
			Namespace: ns,
		},
		Client: k8s.Client().CoordinationV1(),
		LockConfig: resourcelock.ResourceLockConfig{
			Identity: operatorID, // Identity name of the lock holder
		},
	}

	// Start the leader election for running kvstoremesh-operators
	leaderelection.RunOrDie(leaderElectionCtx, leaderelection.LeaderElectionConfig{
		Name: resourceLockName,

		Lock:            resourceLock,
		ReleaseOnCancel: true,

		LeaseDuration: option.Config.LeaderElectionLeaseDuration,
		RenewDeadline: option.Config.LeaderElectionRenewDeadline,
		RetryPeriod:   option.Config.LeaderElectionRetryPeriod,

		Callbacks: leaderelection.LeaderCallbacks{
			OnStartedLeading: onStartLeading,
			OnStoppedLeading: func() {
				log.WithField("kvstoremesh-operator-id", operatorID).Info("Leader election lost")
				// Cleanup everything here, and exit.
				doCleanup()
			},
			OnNewLeader: func(identity string) {
				if identity == operatorID {
					log.Info("Leading the operator HA deployment")
				} else {
					log.WithFields(logrus.Fields{
						"newLeader":  identity,
						"operatorID": operatorID,
					}).Info("Leader re-election complete")
				}
			},
		},
	})
}

func initK8s(k8sInitDone chan struct{}) {
	k8s.Configure(option.Config.K8sAPIServer,
		option.Config.K8sKubeConfigPath,
		float32(option.Config.K8sClientQPSLimit),
		option.Config.K8sClientBurst,
	)

	if err := k8s.Init(ciliumOption.Config); err != nil {
		log.WithError(err).Fatal("Unable to connect to Kubernetes apiserver")
	}

	close(k8sInitDone)
}

// onStartLeading is the function called once the operator starts leading
// in HA mode.
func onStartLeading(ctx context.Context) {
	isLeader.Store(true)

	// Init KVStoreMesh
	log.Info("Initializing KVStoreMesh subsystem")
	_, err := kvstoremesh.NewKVStoreMesh(kvstoremesh.Configuration{Name: "kvstoremesh-operator"})
	if err != nil {
		log.WithError(err).Fatalf("Init KVStoreMesh failed")
	}

	log.Info("Init KVStoreMesh successful")

	<-shutdownSignal
	log.Info("Received termination signal. Shutting down")
}

func doCleanup() {
	isLeader.Store(false)
	gops.Close()
	close(shutdownSignal)
	leaderElectionCtxCancel()
}
