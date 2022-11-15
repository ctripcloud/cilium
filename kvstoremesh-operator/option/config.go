// SPDX-License-Identifier: Apache-2.0
// Copyright 2022 Authors of Cilium

package option

import (
	"fmt"
	"os"
	"time"

	"github.com/cilium/cilium/kvstoremesh-operator/defaults"
	"github.com/cilium/cilium/pkg/kvstore"
	"github.com/cilium/cilium/pkg/logging"
	"github.com/cilium/cilium/pkg/logging/logfields"
	ciliumOption "github.com/cilium/cilium/pkg/option"

	"github.com/spf13/viper"
)

var log = logging.DefaultLogger.WithField(logfields.LogSubsys, "option")

const (
	// ConfigDir is the directory that contains a file for each option where
	// the filename represents the option name and the content of that file
	// represents the value of that option.
	ConfigDir = "config-dir"

	// ClusterName is the name of the ClusterName option
	ClusterName = "cluster-name"

	// ClusterIDName is the name of the ClusterID option
	ClusterIDName = "cluster-id"

	/////////////////////////////////////////////////////////////////////
	// KVStore options
	/////////////////////////////////////////////////////////////////////

	// KVStore key-value store type
	KVStore = "kvstore"

	// KVStoreOpt key-value store options
	KVStoreOpt = "kvstore-opt"

	// KVstoreLeaseTTL is the time-to-live for lease in kvstore.
	KVstoreLeaseTTL = "kvstore-lease-ttl"

	// KVstoreMaxConsecutiveQuorumErrorsName is the maximum number of acceptable
	// kvstore consecutive quorum errors before the agent assumes permanent failure
	KVstoreMaxConsecutiveQuorumErrorsName = "kvstore-max-consecutive-quorum-errors"

	// RemoteKVStoresConfigDirName is the name of the RemoteKVStoresConfigDir option
	RemoteKVStoresConfigDirName = "remote-kvstores-config-dir"

	/////////////////////////////////////////////////////////////////////
	// API options
	/////////////////////////////////////////////////////////////////////

	// APIServeAddr IP:Port on which to serve api requests in
	// operator (pass ":Port" to bind on all interfaces, "" is off)
	APIServeAddr = "api-serve-addr"

	// PrometheusServeAddr IP:Port on which to serve prometheus
	// metrics (pass ":Port" to bind on all interfaces, "" is off).
	PrometheusServeAddr = "prometheus-serve-addr"

	// GopsPort is the TCP port for the gops server.
	GopsPort = "gops-port"

	/////////////////////////////////////////////////////////////////////
	// K8s options
	/////////////////////////////////////////////////////////////////////

	// K8sKubeConfigPath is the absolute path of the kubernetes kubeconfig file
	//
	// The precedence of the configuration used for create K8s client:
	// 1. kubeCfgPath
	// 2. apiServerURL (https if specified)
	// 3. rest.InClusterConfig(): use pod ENV `KUBERNETES_SERVICE_HOST` and `KUBERNETES_SERVICE_PORT`
	//
	// See kg/k8s/client.go for more details.
	K8sKubeConfigPath = "k8s-kubeconfig-path"

	// K8sAPIServer is the kubernetes api address server (for https use --k8s-kubeconfig-path instead)
	K8sAPIServer = "k8s-api-server"

	// K8sSyncTimeout is the timeout to synchronize all resources with k8s.
	K8sSyncTimeoutName = "k8s-sync-timeout"

	// K8sClientQPSLimit is the queries per second limit for the K8s client. Defaults to k8s client defaults.
	K8sClientQPSLimit = "k8s-client-qps"

	// K8sClientBurst is the burst value allowed for the K8s client. Defaults to k8s client defaults.
	K8sClientBurst = "k8s-client-burst"

	/////////////////////////////////////////////////////////////////////
	// Leader election options
	/////////////////////////////////////////////////////////////////////

	// LeaderElectionLeaseDuration is the duration that non-leader candidates will wait to
	// force acquire leadership
	LeaderElectionLeaseDuration = "leader-election-lease-duration"

	// LeaderElectionRenewDeadline is the duration that the current acting master in HA deployment
	// will retry refreshing leadership before giving up the lock.
	LeaderElectionRenewDeadline = "leader-election-renew-deadline"

	// LeaderElectionRetryPeriod is the duration the LeaderElector clients should wait between
	// tries of the actions in operator HA deployment.
	LeaderElectionRetryPeriod = "leader-election-retry-period"

	/////////////////////////////////////////////////////////////////////
	// Misc options
	/////////////////////////////////////////////////////////////////////

	// DebugArg is the argument enables debugging mode
	DebugArg = "debug"

	// PProf enables serving the pprof debugging API
	PProf = "pprof"

	// PProfPort is the port that the pprof listens on
	PProfPort = "pprof-port"

	// LogDriver sets logging endpoints to use for example syslog, fluentd
	LogDriver = "log-driver"

	// LogOpt sets log driver options for cilium
	LogOpt = "log-opt"

	// Version prints the version information
	Version = "version"
)

// Config is the configuration used by the operator.
type KVStoreMeshConfig struct {
	ConfigDir   string
	ClusterName string
	ClusterID   int

	KVStore                           string
	KVStoreOpt                        map[string]string
	KVstoreLeaseTTL                   time.Duration
	KVstoreMaxConsecutiveQuorumErrors int

	RemoteKVStoresConfigDir string

	APIServeAddr        string
	PrometheusServeAddr string
	GopsPort            int

	K8sAPIServer      string
	K8sKubeConfigPath string
	K8sClientBurst    int
	K8sClientQPSLimit float64
	K8sSyncTimeout    time.Duration

	LeaderElectionLeaseDuration time.Duration
	LeaderElectionRenewDeadline time.Duration
	LeaderElectionRetryPeriod   time.Duration

	Debug     bool
	PProf     bool
	PProfPort int
	LogDriver []string
	LogOpt    map[string]string
	Version   string

	// Calculated fields
	IdentityStart int
	IdentityEnd   int
}

const (
	KVStoreTypeEtcd = "etcd"
)

// Populate sets all options with the values from viper.
func (c *KVStoreMeshConfig) Populate() {
	c.Debug = viper.GetBool(DebugArg)
	c.PProf = viper.GetBool(PProf)
	c.PProfPort = viper.GetInt(PProfPort)

	// Cluster options
	c.ClusterID = viper.GetInt(ClusterIDName)
	c.ClusterName = viper.GetString(ClusterName)

	// K8s options
	c.K8sKubeConfigPath = viper.GetString(K8sKubeConfigPath)
	c.K8sAPIServer = viper.GetString(K8sAPIServer)
	c.K8sClientBurst = viper.GetInt(K8sClientBurst)
	c.K8sClientQPSLimit = viper.GetFloat64(K8sClientQPSLimit)

	// KVStore options
	c.KVStore = viper.GetString(KVStore)
	if m := viper.GetStringMapString(KVStoreOpt); len(m) != 0 {
		c.KVStoreOpt = m
	}

	c.KVstoreLeaseTTL = viper.GetDuration(KVstoreLeaseTTL)
	c.KVstoreMaxConsecutiveQuorumErrors = viper.GetInt(KVstoreMaxConsecutiveQuorumErrorsName)

	if s := viper.GetString(RemoteKVStoresConfigDirName); s != "" {
		c.RemoteKVStoresConfigDir = s
	}

	// Log options
	c.LogDriver = viper.GetStringSlice(LogDriver)
	if m := viper.GetStringMapString(LogOpt); len(m) != 0 {
		c.LogOpt = m
	}

	c.APIServeAddr = viper.GetString(APIServeAddr)
	c.PrometheusServeAddr = viper.GetString(PrometheusServeAddr)
	c.LeaderElectionLeaseDuration = viper.GetDuration(LeaderElectionLeaseDuration)
	c.LeaderElectionRenewDeadline = viper.GetDuration(LeaderElectionRenewDeadline)
	c.LeaderElectionRetryPeriod = viper.GetDuration(LeaderElectionRetryPeriod)

	c.K8sSyncTimeout = viper.GetDuration(K8sSyncTimeoutName)

	// Init pkg/option.Config as the code directly accesses them, give us no
	// chance to pass other values to the methods
	ciliumOption.Config.KVstoreLeaseTTL = c.KVstoreLeaseTTL
}

func (c *KVStoreMeshConfig) Validate() error {
	if c.ClusterName == defaults.ClusterName {
		return fmt.Errorf("%s=%s: can't use default cluster name. Custom and"+
			" distinct cluster names and ids are needed for running KVStoreMesh",
			ClusterName, c.ClusterName)
	}

	if c.ClusterID == 0 {
		return fmt.Errorf("%s=%d: can't use default cluster ID. Custom and"+
			" distinct cluster names and ids are needed for running KVStoreMesh",
			ClusterIDName, c.ClusterID)
	}

	// K8s options
	if c.KVStore != KVStoreTypeEtcd {
		return fmt.Errorf("kvstore=%s, currently only support: kvstore=%s", c.KVStore, KVStoreTypeEtcd)
	}

	// KVStore options
	if _, ok := c.KVStoreOpt[kvstore.EtcdOptionConfig]; !ok {
		return fmt.Errorf("'%s' not provided in kvstore-opt='%s'", kvstore.EtcdOptionConfig, c.KVStoreOpt)
	}

	if c.KVstoreLeaseTTL > defaults.KVstoreLeaseMaxTTL || c.KVstoreLeaseTTL < defaults.LockLeaseTTL {
		return fmt.Errorf("KVstoreLeaseTTL does not lie in required range(%ds, %ds)",
			int64(defaults.LockLeaseTTL.Seconds()),
			int64(defaults.KVstoreLeaseMaxTTL.Seconds()))
	}

	// Init other fields
	identityStart, identityEnd, err := GetClusterIdentityRange(c.ClusterID)
	if err != nil {
		return fmt.Errorf("Get identity range for local cluster failed: %v", err)
	}

	c.IdentityStart = identityStart
	c.IdentityEnd = identityEnd

	return nil
}

// Config represents the operator configuration.
var Config = &KVStoreMeshConfig{
	RemoteKVStoresConfigDir: defaults.RemoteKVStoresConfigDir,
	K8sClientBurst:          defaults.K8sClientBurst,
	K8sClientQPSLimit:       defaults.K8sClientQPSLimit,
	KVStoreOpt: map[string]string{
		kvstore.EtcdOptionConfig: "/var/lib/etcd-config/etcd.config",
	},
}

// InitConfig reads in config file and ENV variables if set.
func InitConfig(progName string) func() {
	return func() {
		if viper.GetBool("version") {
			fmt.Printf("%s %s\n", progName, Config.Version)
			os.Exit(0)
		}

		configDir := viper.GetString(ConfigDir)
		Config.ConfigDir = configDir
		if configDir == "" {
			log.Info("Skip reading configmap, --config-dir not provided")
			return
		}

		if _, err := os.Stat(configDir); os.IsNotExist(err) {
			log.Fatalf("Non-existent configuration directory %s", configDir)
		}

		if m, err := ciliumOption.ReadDirConfig(configDir); err != nil {
			log.WithError(err).Fatalf("Unable to read configuration directory %s", configDir)
		} else {
			err := viper.MergeConfigMap(m)
			if err != nil {
				log.WithError(err).Fatal("Unable to merge configuration")
			}
		}
	}
}

// GetClusterIdentityRange returns the correct identity range of the cluster
func GetClusterIdentityRange(clusterID int) (int, int, error) {
	if clusterID > 255 || clusterID < 0 {
		return 0, 0, fmt.Errorf("invalid cluster id %d, supported range [0, 255]", clusterID)
	}

	start := clusterID<<16 + 0
	return start, start + 65535, nil
}
