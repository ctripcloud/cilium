// SPDX-License-Identifier: Apache-2.0
// Copyright 2022 Authors of Cilium

package kvstoremesh

import (
	"context"
	"fmt"

	"github.com/cilium/cilium/kvstoremesh-operator/option"
	"github.com/cilium/cilium/pkg/controller"
	"github.com/cilium/cilium/pkg/kvstore"
	"github.com/cilium/cilium/pkg/lock"
	"github.com/cilium/cilium/pkg/logging"
	"github.com/cilium/cilium/pkg/logging/logfields"

	"github.com/sirupsen/logrus"
)

const (
	// configNotifChannelSize is the size of the channel used to notify
	// kvstoremesh configuration changes
	configNotifChannelSize = 512

	// Log fields
	fieldRemoteClusterName = "remoteClusterName"
	fieldConfig            = "config"
	fieldClusterID         = "clusterID"
	fieldIdentityStart     = "identityStart"
	fieldIdentityEnd       = "identityEnd"
	fieldKey               = "key"
	fieldValue             = "value"
	fieldError             = "error"
	fieldPrefix            = "prefix"
	fieldElapsedTime       = "elapsedTime"
	fieldEventType         = "eventType"
)

var log = logging.DefaultLogger.WithField(logfields.LogSubsys, "kvstoremesh")

// Configuration is the configuration that provided to NewKVStoreMesh()
type Configuration struct {
	// Name is the name of the kvstoremesh, for logging purposes only
	Name string
}

// KVStoreMesh is a cache of multiple remote clusters
type KVStoreMesh struct {
	// conf is the configuration, it is immutable after NewKVStoreMesh()
	conf Configuration

	// kvstoreBackend is the interafce to operate the local kvstore
	kvstoreBackend kvstore.BackendOperations
	kvstoreMutex   lock.RWMutex

	mutex             lock.RWMutex
	remoteClusters    map[string]*remoteCluster
	controllerManager *controller.Manager
	configWatcher     *configDirectoryWatcher
}

// NewKVStoreMesh creates a new puller to sync specified entries from
// remote kvstores to the local one
func NewKVStoreMesh(c Configuration) (*KVStoreMesh, error) {
	// Create client to local kvstore
	backend, errCh := kvstore.NewClient(context.TODO(),
		kvstore.EtcdBackendName,
		option.Config.KVStoreOpt,
		&kvstore.ExtraOptions{NoLockQuorumCheck: true})

	// Block until either an error is returned or the channel is closed due to
	// success of the connection
	log.Info("Connecting to local kvstore ...")
	if err, isErr := <-errCh; isErr {
		if backend != nil {
			backend.Close()
		}
		log.WithError(err).Error("connect to local kvstore failed")
		return nil, err
	}

	log.Info("Connect to local kvstore successful")

	km := &KVStoreMesh{
		conf:              c,
		remoteClusters:    map[string]*remoteCluster{},
		controllerManager: controller.NewManager(),
		kvstoreBackend:    backend,
	}

	configDir := option.Config.RemoteKVStoresConfigDir
	w, err := createConfigDirWatcher(configDir, km)
	if err != nil {
		return nil, fmt.Errorf("create configdir watcher for %s failed: %s", configDir, err)
	}

	km.configWatcher = w
	if err := km.configWatcher.watch(); err != nil {
		return nil, fmt.Errorf("watch configdir %s failed: %v", configDir, err)
	}

	return km, nil
}

// Close stops watching for remote cluster configuration files to appear and
// will close all connections to remote clusters
func (km *KVStoreMesh) Close() {
	km.mutex.Lock()
	defer km.mutex.Unlock()

	if km.configWatcher != nil {
		km.configWatcher.close()
	}

	for name, cluster := range km.remoteClusters {
		cluster.onRemove()
		delete(km.remoteClusters, name)
	}

	km.controllerManager.RemoveAllAndWait()
}

func (km *KVStoreMesh) Logger() *logrus.Entry {
	return log.WithFields(logrus.Fields{
		"localClusterName": option.Config.ClusterName,
	})
}

func (km *KVStoreMesh) add(name, path string) {
	scopedLog := km.Logger().WithField(fieldRemoteClusterName, name)

	if name == option.Config.ClusterName {
		scopedLog.Warn("Ignoring configuration for own cluster")
		return
	}

	inserted := false

	km.mutex.Lock()
	cluster, ok := km.remoteClusters[name]
	if !ok {
		cluster = newRemoteCluster(name, path, km)
		km.remoteClusters[name] = cluster
		inserted = true
	}
	km.mutex.Unlock()

	scopedLog.WithField("isNewFile", inserted).Info("Remote cluster configuration added")

	if inserted {
		cluster.onInsert()
	} else {
		scopedLog.Info("Remote cluster configuration file changed")
	}
}

func (km *KVStoreMesh) remove(name string) {
	km.mutex.Lock()
	if cluster, ok := km.remoteClusters[name]; ok {
		cluster.onRemove()
		delete(km.remoteClusters, name)
	}
	km.mutex.Unlock()

	km.Logger().WithField(fieldRemoteClusterName, name).Info("Remote cluster configuration removed")
}

// NumReadyClusters returns the number of remote clusters to which a connection
// has been established
func (km *KVStoreMesh) NumReadyClusters() int {
	km.mutex.RLock()
	defer km.mutex.RUnlock()

	nready := 0
	for _, cluster := range km.remoteClusters {
		if cluster.isReady() {
			nready++
		}
	}

	return nready
}
