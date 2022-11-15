// SPDX-License-Identifier: Apache-2.0
// Copyright 2022 Authors of Cilium

package kvstoremesh

import (
	"context"
	"fmt"
	"io/ioutil"
	"path"
	"strconv"
	"time"

	"github.com/cilium/cilium/api/v1/models"
	"github.com/cilium/cilium/kvstoremesh-operator/option"
	"github.com/cilium/cilium/pkg/controller"
	identityCache "github.com/cilium/cilium/pkg/identity/cache"
	"github.com/cilium/cilium/pkg/ipcache"
	"github.com/cilium/cilium/pkg/kvstore"
	"github.com/cilium/cilium/pkg/lock"
	nodeStore "github.com/cilium/cilium/pkg/node/store"
	serviceStore "github.com/cilium/cilium/pkg/service/store"

	strfmt "github.com/go-openapi/strfmt"
	"github.com/sirupsen/logrus"
	"sigs.k8s.io/yaml"
)

// remoteClusterConfig is the yaml configuration file for a remote cluster
type remoteClusterConfig struct {
	ClusterName      string `json:"cluster-name"`
	ClusterID        string `json:"cluster-id"`
	EtcdOptionConfig string `json:"etcd.config"`
	EtcdRateLimit    string `json:"etcd.qps"`
}

// remoteCluster represents a remote cluster other than the local one this
// service is running in
type remoteCluster struct {
	clusterName string
	clusterID   int

	// configPath is the path to the cluster configuration file of this cluster
	configPath string

	// configChangedCh receives events when the cluster configuration changed
	configChangedCh chan bool

	// etcdConfigPath path for the kvstore of the remote cluster
	etcdConfigPath string

	// etcdRateLimit (QPS) for the kvstore of the remote cluster
	etcdRateLimit string

	// backend is the kvstore backend being used
	backend kvstore.BackendOperations

	// kvstoremesh is the kvstoremesh this remote cluster belongs to
	kvstoremesh *KVStoreMesh

	// Prefixes of keys in kvstore
	identityMasterKeyPrefix string
	identitySlaveKeyPrefix  string
	ipKeyPrefix             string
	nodeKeyPrefix           string
	serviceKeyPrefix        string

	controllerManager *controller.Manager

	// remoteConnectionControllerName is the name of the backing controller
	// that maintains the remote connection
	remoteConnectionControllerName string

	// mutex protects the following variables
	// - backend
	mutex lock.RWMutex

	// failures is the number of observed failures
	failures int

	// lastFailure is the timestamp of the last failure
	lastFailure time.Time

	identityStart int
	identityEnd   int
}

var (
	// skipKvstoreConnection skips the etcd connection, used for testing
	skipKvstoreConnection bool
)

func newRemoteCluster(clusterName, configPath string, km *KVStoreMesh) *remoteCluster {
	scopedLog := log.WithFields(logrus.Fields{
		fieldRemoteClusterName: clusterName,
		fieldConfig:            configPath,
	})

	b, err := ioutil.ReadFile(configPath)
	if err != nil {
		scopedLog.WithError(err).Error("Read config file failed")
		return nil
	}

	yamlConfig := &remoteClusterConfig{}
	if err := yaml.Unmarshal(b, yamlConfig); err != nil {
		scopedLog.WithError(err).Error("Unmarshal config file failed")
		return nil
	}

	clusterID, err := strconv.Atoi(yamlConfig.ClusterID)
	if err != nil {
		scopedLog.WithError(err).Errorf("Convert cluster-id '%s' to int failed", yamlConfig.ClusterID)
		return nil
	}

	identityStart, identityEnd, err := option.GetClusterIdentityRange(clusterID)
	if err != nil {
		scopedLog.WithError(err).Errorf("Get identity range failed")
		return nil
	}

	etcdRateLimit := "20"
	if yamlConfig.EtcdRateLimit != "" {
		etcdRateLimit = yamlConfig.EtcdRateLimit
	}

	rc := &remoteCluster{
		clusterName:             clusterName,
		clusterID:               clusterID,
		configPath:              configPath,
		identityStart:           identityStart,
		identityEnd:             identityEnd,
		etcdConfigPath:          yamlConfig.EtcdOptionConfig,
		etcdRateLimit:           etcdRateLimit,
		kvstoremesh:             km,
		identityMasterKeyPrefix: path.Join(identityCache.IdentitiesPath, "id") + "/",
		identitySlaveKeyPrefix:  path.Join(identityCache.IdentitiesPath, "value") + "/",
		ipKeyPrefix:             path.Join(ipcache.IPIdentitiesPath, clusterName) + "/",
		nodeKeyPrefix:           path.Join(nodeStore.NodeStorePrefix, clusterName) + "/",
		serviceKeyPrefix:        path.Join(serviceStore.ServiceStorePrefix, clusterName) + "/",
		configChangedCh:         make(chan bool, configNotifChannelSize),
		controllerManager:       controller.NewManager(),
	}

	scopedLog.WithFields(logrus.Fields{
		fieldClusterID:            clusterID,
		"identityStart":           identityStart,
		"identityEnd":             identityEnd,
		"identityMasterKeyPrefix": rc.identityMasterKeyPrefix,
		"identitySlaveKeyPrefix":  rc.identitySlaveKeyPrefix,
		"ipKeyPrefix":             rc.ipKeyPrefix,
		"nodeKeyPrefix":           rc.nodeKeyPrefix,
		"serviceKeyPrefix":        rc.serviceKeyPrefix,
	}).Info("New remote cluster")

	return rc
}

func (rc *remoteCluster) Logger() *logrus.Entry {
	return log.WithFields(logrus.Fields{
		"remoteClusterName": rc.clusterName,
		"remoteClusterID":   rc.clusterID,
	})
}

// releaseOldConnection releases the etcd connection to a remote cluster
func (rc *remoteCluster) releaseOldConnection() {
	rc.mutex.Lock()
	backend := rc.backend
	rc.backend = nil
	rc.mutex.Unlock()

	// Release resources asynchroneously in the background. Many of these
	// operations may time out if the connection was closed due to an error
	// condition.
	go func() {
		if backend != nil {
			backend.Close()
		}
	}()
}

func (rc *remoteCluster) restartRemoteConnection() {
	rc.controllerManager.UpdateController(rc.remoteConnectionControllerName, controller.ControllerParams{
		DoFunc: func(ctx context.Context) error {
			rc.releaseOldConnection()

			backend, errCh := kvstore.NewClient(context.TODO(),
				kvstore.EtcdBackendName,
				map[string]string{
					kvstore.EtcdOptionConfig:    rc.etcdConfigPath,
					kvstore.EtcdRateLimitOption: rc.etcdRateLimit,
				},
				&kvstore.ExtraOptions{NoLockQuorumCheck: true})

			// Block until either an error is returned or the channel is
			// closed due to success of the connection
			rc.Logger().Info("Connecting to remote kvstore ...")
			if err, isErr := <-errCh; isErr {
				if backend != nil {
					backend.Close()
				}
				rc.Logger().WithError(err).Error("Connect to remote kvstore failed")
				return err
			}

			rc.Logger().Info("Connect to remote kvstore successful")

			rc.mutex.Lock()
			rc.backend = backend
			rc.mutex.Unlock()

			go rc.reconcileNodeEntries()           // Key->Value: NodeName -> NodeDetails
			go rc.reconcileIPEntries()             // Key->Value: PodIP -> IPDetails
			go rc.reconcileIdentityMasterEntries() // Key->Value: IdentityID -> PodLabels
			go rc.reconcileIdentitySlaveEntries()  // Key->Value: PodLabels+NodeIP -> IdentityID
			go rc.reconcileServiceEntries()        // Key->Value: ServiceName -> ServiceDetails

			return nil
		},
		StopFunc: func(ctx context.Context) error {
			rc.releaseOldConnection()
			rc.Logger().Info("All resources of remote kvstore cleaned up")
			return nil
		},
	})
}

func (rc *remoteCluster) onInsert() {
	rc.Logger().Info("New remote kvstore configuration")

	if skipKvstoreConnection {
		return
	}

	rc.remoteConnectionControllerName = fmt.Sprintf("kvstoremesh-remote-cluster-%s", rc.clusterName)
	rc.restartRemoteConnection()

	go func() {
		for {
			val := <-rc.configChangedCh
			if val {
				rc.Logger().Info("etcd configuration has changed, re-creating connection")
				rc.restartRemoteConnection()
			} else {
				rc.Logger().Info("Closing connection to remote etcd")
				return
			}
		}
	}()

	go func() {
		for {
			select {
			// terminate routine when remote cluster is removed
			case _, ok := <-rc.configChangedCh:
				if !ok {
					return
				}
			default:
			}

			// wait for backend to appear
			rc.mutex.RLock()
			if rc.backend == nil {
				rc.mutex.RUnlock()
				time.Sleep(10 * time.Millisecond)
				continue
			}
			statusCheckErrors := rc.backend.StatusCheckErrors()
			rc.mutex.RUnlock()

			err, ok := <-statusCheckErrors
			if ok && err != nil {
				rc.Logger().WithError(err).Warning("Error observed on etcd connection, reconnecting etcd")
				rc.mutex.Lock()
				rc.failures++
				rc.lastFailure = time.Now()
				rc.mutex.Unlock()
				rc.restartRemoteConnection()
			}
		}
	}()

}

func (rc *remoteCluster) onRemove() {
	rc.controllerManager.RemoveAllAndWait()
	close(rc.configChangedCh)

	rc.Logger().Info("Remote cluster disconnected")
}

func (rc *remoteCluster) isReady() bool {
	rc.mutex.RLock()
	defer rc.mutex.RUnlock()

	return rc.isReadyLocked()
}

func (rc *remoteCluster) isReadyLocked() bool {
	return rc.backend != nil
}

func (rc *remoteCluster) status() *models.RemoteCluster {
	rc.mutex.RLock()
	defer rc.mutex.RUnlock()

	// This can happen when the controller in restartRemoteConnection is waiting
	// for the first connection to succeed.
	var backendStatus = "Waiting for initial connection to be established"
	if rc.backend != nil {
		var backendError error
		backendStatus, backendError = rc.backend.Status()
		if backendError != nil {
			backendStatus = backendError.Error()
		}
	}

	return &models.RemoteCluster{
		Name:        rc.clusterName,
		Ready:       rc.isReadyLocked(),
		Status:      backendStatus,
		NumFailures: int64(rc.failures),
		LastFailure: strfmt.DateTime(rc.lastFailure),
	}
}
