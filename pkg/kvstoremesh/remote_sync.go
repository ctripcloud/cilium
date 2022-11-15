// SPDX-License-Identifier: Apache-2.0
// Copyright 2022 Authors of Cilium

package kvstoremesh

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/cilium/cilium/kvstoremesh-operator/metrics"
	"github.com/cilium/cilium/kvstoremesh-operator/option"
	"github.com/cilium/cilium/pkg/kvstore"

	"github.com/sirupsen/logrus"
)

// Types of alien kvstore keys we're interested in
const (
	TypeNodeKey           = "nodes"
	TypeIPKey             = "ip"
	TypeServiceKey        = "services"
	TypeIdentityMasterKey = "identities-master-key"
	TypeIdentitySlaveKey  = "identities-slave-key"

	ciliumPolicyClusterLabelFmt = "k8s:io.cilium.k8s.policy.cluster=%s;"
)

type AlienKeyInfo struct {
	KeyPattern string
	NumFields  int // Number of fields when split the keys with '/'
}

// Keys of the kvstore entries we'll sync
var KeyPatterns = map[string]AlienKeyInfo{
	TypeNodeKey:           {"cilium/state/nodes/v1/<clustername>/<nodename>", 6},
	TypeIPKey:             {"cilium/state/ip/v1/<clustername>/<ip>", 6},
	TypeIdentityMasterKey: {"cilium/state/identities/v1/id/<id>", 6},

	// The labels in this key may also contain '/' character. So 7 is the least length.
	TypeIdentitySlaveKey: {"cilium/state/identities/v1/value/<label1;label2;labelN>;/<IP>", 7},
}

// AlienEntryController list-watches specific entries from a remote kvstore,
// and deposits them into the local kvstore.
type AlienEntryController struct {
	// prefix is the common prefix of the keys this controller is managing
	prefix string

	// remoteCluster is the remote cluster this controller relates to
	remoteCluster *remoteCluster

	// Validate validates a given alien key, preventing from conflicting with
	// native keys in the local kvstore
	Validate func(*remoteCluster, ValidateContext) (bool, error)

	// GC perform garbage collection of alien keys in the local kvstore
	GC func(*remoteCluster, kvstore.KeyValuePairs) *GCStats
}

type ValidateContext struct {
	eventType kvstore.EventType

	key string
	val string

	// localKVStoreBackend is the backend of the local kvstore
	localKVStoreBackend kvstore.BackendOperations
}

// GCStats is the statistics after one alien-entries GC run
type GCStats struct {
	deleted int
	alive   int

	elapsedTime time.Duration
}

// reconcileNodeEntries watches TypeNodeKey entries in the remote kvstore, and
// synchronizes them to the local kvstore.
//
// kvstore entry example:
// K: cilium/state/nodes/v1/cluster1/node1
// V: {"Name":"node1","Cluster":"cluster1",...,"Labels":{"kubernetes.io/hostname":"node1"},"NodeIdentity":6}
func (rc *remoteCluster) reconcileNodeEntries() {
	c := AlienEntryController{
		prefix:        rc.nodeKeyPrefix,
		remoteCluster: rc,
		Validate:      validateNodeEntry,
		GC:            gcClusternamePrefixedEntries,
	}

	c.Reconcile()
}

func validateNodeEntry(rc *remoteCluster, ctx ValidateContext) (bool, error) {
	// Nothing to check, as the keys already has clustername in prefix, and
	// we list+watch entries with that prefix
	return true, nil
}

// gcClusternamePrefixedEntries performs GC against alien entries that have
// cluster-scoped keys, the following keys to be specific:
//
// * cilium/state/nodes/v1/<clustername>/<nodename>
// * cilium/state/ip/v1/<clustername>/<ip>
// * cilium/state/services/v1/<clustername>/<ip>
func gcClusternamePrefixedEntries(rc *remoteCluster, alienKVs kvstore.KeyValuePairs) *GCStats {
	localKVStoreBackend := rc.kvstoremesh.kvstoreBackend
	remoteKVStoreBackend := rc.backend
	scopedLog := rc.Logger()

	total := 0
	deleted := 0
	startTime := time.Now()
	ctx := context.TODO()
	for k := range alienKVs {
		total++

		// Include "remoteClusterName", "key", "method" info in gc log
		gcLog := scopedLog.WithFields(logrus.Fields{
			fieldKey: k,
			"method": "gcClusternamePrefixedEntries",
		})

		// Perform GC. The following steps must be in a transaction:
		// 1. Make sure the key no longer exists in the remote kvstore
		// 2. Delete the key from local kvstore if it exists
		rc.kvstoremesh.kvstoreMutex.Lock()
		if _, err := remoteKVStoreBackend.Get(ctx, k); err != nil {
			gcLog.WithError(err).Error("Get key from remote kvstore failed")
			rc.kvstoremesh.kvstoreMutex.Unlock()
			continue
		}
		if err := localKVStoreBackend.Delete(ctx, k); err != nil {
			gcLog.WithError(err).Error("Delete key from local kvstore failed")
			rc.kvstoremesh.kvstoreMutex.Unlock()
			continue
		}
		rc.kvstoremesh.kvstoreMutex.Unlock()

		deleted++
		gcLog.WithField(fieldKey, k).Info("Deleted orphaned alien entry from local kvstore")
	}

	return &GCStats{
		deleted:     deleted,
		alive:       total - deleted,
		elapsedTime: time.Since(startTime),
	}
}

// Example of PodIP entries in kvstore:
// K: cilium/state/ip/v1/cluster1/10.5.1.9
// V: {"IP":"10.5.1.9","Mask":null,"HostIP":"10.6.1.3","ID":543510,...}
func (rc *remoteCluster) reconcileIPEntries() {
	c := AlienEntryController{
		prefix:        rc.ipKeyPrefix,
		remoteCluster: rc,
		Validate:      validateIPEntry,
		GC:            gcClusternamePrefixedEntries,
	}

	c.Reconcile()
}

func validateIPEntry(rc *remoteCluster, ctx ValidateContext) (bool, error) {
	// Nothing to check, as the keys already has clustername in prefix, and
	// we list+watch entries with that prefix
	return true, nil
}

// Example of identity (master) entries in kvstore:
// K: cilium/state/identities/v1/id/535498
// V: k8s:io.cilium.k8s.policy.cluster=cluster1;k8s:xxx.pod.namespace=kube-system;
func (rc *remoteCluster) reconcileIdentityMasterEntries() {
	c := AlienEntryController{
		prefix:        rc.identityMasterKeyPrefix,
		remoteCluster: rc,
		Validate:      validateIdentityMasterEntry,
		GC:            gcIdentityMasterEntries,
	}

	c.Reconcile()
}

// KVStore entry example:
// K: cilium/state/identities/v1/id/535498
func validateIdentityMasterEntry(rc *remoteCluster, ctx ValidateContext) (bool, error) {
	key := ctx.key
	keyInfo := KeyPatterns[TypeIdentityMasterKey]

	items := strings.Split(key, "/")
	if len(items) != keyInfo.NumFields {
		return false, fmt.Errorf("unexpected format of identities/v1/id key: %v", key)
	}

	i, err := strconv.Atoi(items[keyInfo.NumFields-1])
	if err != nil {
		return false, fmt.Errorf("convert identity to int failed: %v", key)
	}

	// Each controller should only handle keys within its own scope
	if i >= rc.identityStart && i <= rc.identityEnd {
		return true, nil
	}

	log.Debugf("identity %d out of responsible scope [%d, %d]", i,
		rc.identityStart, rc.identityEnd)
	return false, nil
}

// Make sure this patch is applied: https://github.com/cilium/cilium/pull/16825
func gcIdentityMasterEntries(rc *remoteCluster, alienKVs kvstore.KeyValuePairs) *GCStats {
	localKVStoreBackend := rc.kvstoremesh.kvstoreBackend
	remoteKVStoreBackend := rc.backend
	keyInfo := KeyPatterns[TypeIdentityMasterKey]

	// Embed "remoteClusterName", "key", "method" info into GC log
	gcLog := rc.Logger().WithField("method", "gcIdentityMasterEntries")
	gcStats := GCStats{}

	deleted := 0
	total := 0
	startTime := time.Now()
	ctx := context.TODO()
	remoteClusternameLabel := fmt.Sprintf(ciliumPolicyClusterLabelFmt, rc.clusterName)

	for k, v := range alienKVs {
		total++

		// For safety concerns, validate the given input again. Key first.
		// Validate key: identity should be in cluster identity range
		items := strings.Split(k, "/")
		if len(items) != keyInfo.NumFields {
			gcLog.WithField(fieldKey, k).Error("Unexpected format of identities/v1/id key")
			continue
		}

		i, err := strconv.Atoi(items[keyInfo.NumFields-1])
		if err != nil {
			gcLog.WithField(fieldKey, k).Error("Convert identity to int failed")
			continue
		}

		if i < rc.identityStart || i > rc.identityEnd {
			gcLog.WithFields(logrus.Fields{
				fieldKey:        k,
				"identity":      i,
				"identityStart": rc.identityStart,
				"identityEnd":   rc.identityEnd,
			}).Error("Identity out of cluster identity range")
			continue
		}

		// Identity master key is valid, now double confirm by the value.
		if !strings.Contains(string(v.Data), remoteClusternameLabel) {
			gcLog.WithFields(logrus.Fields{
				fieldKey:   k,
				fieldValue: string(v.Data),
			}).Error("Identity master entry doesn't contain expected clustername")
			continue
		}

		// Perform GC. The following steps must be in a transaction:
		// 1. If the key no longer exists in the remote kvstore,
		// 2. Delete the key from local kvstore if it exists
		rc.kvstoremesh.kvstoreMutex.Lock()
		val, err := remoteKVStoreBackend.Get(ctx, k)
		if err != nil {
			gcLog.WithField(fieldKey, k).WithError(err).Warn("Get key from remote kvstore failed, skip to GC the alien entry")
			rc.kvstoremesh.kvstoreMutex.Unlock()
			continue
		}

		if len(val) > 0 { // key found, this may happen in racing conditions
			gcLog.WithField(fieldKey, k).WithError(err).Warn("Key still in remote kvstore, skip to GC the alien entry")
			rc.kvstoremesh.kvstoreMutex.Unlock()
			continue
		}

		if err := localKVStoreBackend.Delete(ctx, k); err != nil {
			gcLog.WithFields(logrus.Fields{
				fieldKey:   k,
				fieldValue: string(v.Data),
			}).WithError(err).Error("Delete alien entry from local kvstore failed")
			rc.kvstoremesh.kvstoreMutex.Unlock()
			continue
		}
		rc.kvstoremesh.kvstoreMutex.Unlock()

		deleted++
		gcLog.WithFields(logrus.Fields{
			fieldKey:   k,
			fieldValue: string(v.Data),
		}).Info("Deleted orphaned alien entry from local kvstore")
	}

	gcStats.deleted = deleted
	gcStats.alive = total - deleted
	gcStats.elapsedTime = time.Since(startTime)
	return &gcStats
}

// Format: PodLabels+NodeIP -> IdentityID
// Key: cilium/state/identities/v1/value/<label1;label2;labelN>;/<IP>
// Val: <identity>
//
// kvstore entry example:
// K: cilium/state/identities/v1/value/k8s:io.cilium.k8s.policy.cluster=cluster1;k8s:xxx.pod.namespace=istio;/10.5.1.6
// V: 546460
func (rc *remoteCluster) reconcileIdentitySlaveEntries() {
	c := AlienEntryController{
		prefix:        rc.identitySlaveKeyPrefix,
		remoteCluster: rc,
		Validate:      validateIdentitySlaveEntry,
		GC:            gcIdentitySlaveEntries,
	}

	c.Reconcile()
}

// K: cilium/state/identities/v1/value/k8s:io.cilium.k8s.policy.cluster=cluster1;k8s:xxx.pod.namespace=istio;/10.6.1.6
//
// Note that val field is not provided when "DELETE key" in this case
func validateIdentitySlaveEntry(rc *remoteCluster, ctx ValidateContext) (bool, error) {
	key := ctx.key
	val := ctx.val
	backend := ctx.localKVStoreBackend
	evType := ctx.eventType

	switch evType {
	case kvstore.EventTypeCreate:
	case kvstore.EventTypeModify:
	case kvstore.EventTypeDelete:
		localClusternameLabel := fmt.Sprintf(ciliumPolicyClusterLabelFmt, option.Config.ClusterName)
		if strings.Contains(key, localClusternameLabel) {
			// When a native entry is deleted from local kvstore, the delete event
			// will propagates to remote kvstores, and the deletion events there
			// will in turn propagate to us, which will hit here.
			// We should just skip this event for efficiency, although try to delete it
			// from local kvstore does no harm (not found).
			log.Infof("Skip delete event: refer to a native entry (should have been deleted just before), key: %s", key)
			return false, nil
		}

		// val (identity) is not provided in DELETE event, so we get it from
		// local kvstore, preventing from deleting a native identity entry
		v, err := backend.Get(context.TODO(), key)
		if err != nil {
			return false, fmt.Errorf("get key %s from local kvstore failed: %v", key, err)
		}

		val = string(v)
		if val == "" { // not found
			log.Warnf("Skip delete event: entry not found in local kvstore, key: %s", key)
			return false, nil
		}
	}

	i, err := strconv.Atoi(val)
	if err != nil {
		return false, fmt.Errorf("convert identity %v to int failed: %v", val, err)
	}

	// Each controller should only handle keys within its own scope
	if i >= rc.identityStart && i <= rc.identityEnd {
		return true, nil
	}

	log.Debugf("identity %d out of responsible scope [%d, %d]", i,
		rc.identityStart, rc.identityEnd)
	return false, nil
}

func gcIdentitySlaveEntries(rc *remoteCluster, alienKVs kvstore.KeyValuePairs) *GCStats {
	localKVStoreBackend := rc.kvstoremesh.kvstoreBackend
	scopedLog := rc.Logger()
	gcStats := GCStats{}

	deleted := 0
	total := 0
	startTime := time.Now()
	ctx := context.TODO()
	for k := range alienKVs {
		total++

		// Clustername is included in the key, such as:
		//   "cilium/state/identities/v1/value/..;k8s:io.cilium.k8s.policy.cluster=<clustername>;...
		// use this info to speedup the GC process.
		remoteClusternameLabel := fmt.Sprintf(ciliumPolicyClusterLabelFmt, rc.clusterName)
		if !strings.Contains(k, remoteClusternameLabel) {
			continue
		}

		// Embed "remoteClusterName", "key", "method" info into GC log
		gcLog := scopedLog.WithFields(logrus.Fields{
			fieldKey: k,
			"method": "gcIdentitySlaveEntries",
		})

		if _, err := rc.backend.Get(ctx, k); err != nil {
			gcLog.WithError(err).Error("Get key from remote kvstore failed")
			continue
		}

		// Confirm the record still exists in local kvstore
		v, err := localKVStoreBackend.Get(ctx, k)
		if err != nil {
			gcLog.WithError(err).Error("Get alien key from local kvstore failed")
			continue
		}

		val := string(v)
		if val == "" {
			gcLog.Info("Alien key deleted from local kvstore before GC")
			continue
		}

		i, err := strconv.Atoi(val)
		if err != nil {
			gcLog.WithFields(logrus.Fields{
				fieldValue: val,
				fieldError: err,
			}).Errorf("Convert identity string to int failed")
			continue
		}

		if i < rc.identityStart || i > rc.identityEnd {
			gcLog.WithField("identity", i).Debugf("Identity out of handling range [%d, %d]",
				rc.identityStart, rc.identityEnd)
			continue
		}

		// Perform GC. The following steps must be in a transaction:
		// 1. Make sure the key no longer exists in the remote kvstore
		// 2. Delete the key from local kvstore if it exists
		rc.kvstoremesh.kvstoreMutex.Lock()
		remoteVal, err := rc.backend.Get(ctx, k)
		if err != nil {
			gcLog.WithError(err).Warn("Get key from remote kvstore failed, skip to GC the alien entry")
			rc.kvstoremesh.kvstoreMutex.Unlock()
			continue
		}

		if len(remoteVal) > 0 { // key found, this may happen in racing conditions
			gcLog.WithField(fieldKey, k).WithError(err).Warn("Key still in remote kvstore, skip to GC the alien entry")
			rc.kvstoremesh.kvstoreMutex.Unlock()
			continue
		}

		if err := localKVStoreBackend.Delete(ctx, k); err != nil {
			gcLog.WithError(err).Error("Delete alien entry from local kvstore failed")
		}
		rc.kvstoremesh.kvstoreMutex.Unlock()

		deleted++
		gcLog.WithField(fieldValue, val).Info("Deleted orphaned alien entry from local kvstore")
	}

	gcStats.deleted = deleted
	gcStats.alive = total - deleted
	gcStats.elapsedTime = time.Since(startTime)
	return &gcStats
}

// K: "cilium/state/services/v1/cluster1/default/cilium-smoke"
// V: "{"cluster":"cluster1","namespace":"default","name":"cilium-smoke","frontends":{"10.7.0.6":{"cilium-smoke":{"Protocol":"TCP","Port":80}}},"backends":{"10.6.1.2":{"cilium-smoke":{"Protocol":"TCP","Port":80}}},"labels":{"app":"cilium-smoke"},"selector":{"app":"cilium-smoke"}}"
func (rc *remoteCluster) reconcileServiceEntries() {
	c := AlienEntryController{
		prefix:        rc.serviceKeyPrefix,
		remoteCluster: rc,
		Validate:      serviceValidate,
		GC:            gcClusternamePrefixedEntries,
	}

	c.Reconcile()
}

func serviceValidate(rc *remoteCluster, ctx ValidateContext) (bool, error) {
	// Nothing to check, as the keys already has clustername in prefix, and
	// we list+watch entries with that prefix
	return true, nil
}

func (m *AlienEntryController) filterIdentityMasterEntries(rc *remoteCluster,
	kvs kvstore.KeyValuePairs, prefix string) (kvstore.KeyValuePairs, error) {

	scopedLog := rc.Logger().WithField(fieldPrefix, prefix)
	keyInfo := KeyPatterns[TypeIdentityMasterKey]
	inRangeKVs := kvstore.KeyValuePairs{}

	for k, v := range kvs {
		items := strings.Split(k, "/")
		if len(items) != keyInfo.NumFields {
			scopedLog.WithField(fieldKey, k).Error("Unexpected format of identities/v1/id key")
			continue
		}

		i, err := strconv.Atoi(items[keyInfo.NumFields-1])
		if err != nil {
			scopedLog.WithField(fieldKey, k).Error("Convert identity to int failed")
			continue
		}

		if i >= rc.identityStart && i <= rc.identityEnd {
			inRangeKVs[k] = v
		}
	}

	total := len(kvs)
	left := len(inRangeKVs)
	// Fool-proofing: should be cautious if we do not filter out any entries:
	// which means all identity master keys in the local kvstore belongs to
	// a single remote kvstore, impossible in real environments!
	if total == left {
		return nil, fmt.Errorf("no master entries are filtered out, it's almost impossible")
	}

	scopedLog.WithFields(logrus.Fields{
		"total": total,
		"left":  left,
	}).Info("Filter identity master entries for remote cluster finished")
	return inRangeKVs, nil
}

func (m *AlienEntryController) Reconcile() {
	rc := m.remoteCluster
	clusterName := rc.clusterName
	prefix := m.prefix
	ctx := context.TODO()
	localKVStoreBackend := rc.kvstoremesh.kvstoreBackend
	scopedLog := rc.Logger().WithField(fieldPrefix, prefix)

	updateToLocalKVStore := func(key string, val []byte) {
		rc.kvstoremesh.kvstoreMutex.Lock()
		if err := localKVStoreBackend.Update(ctx, key, val, false); err != nil {
			rc.Logger().WithFields(logrus.Fields{
				"error":  err,
				fieldKey: key,
			}).Error("Write to local kvstore failed")
		}
		rc.kvstoremesh.kvstoreMutex.Unlock()
	}

	alienKVsLeft, err := localKVStoreBackend.ListPrefix(ctx, prefix)
	if err != nil {
		scopedLog.WithError(err).Error("List alien entries from local kvstore failed")
		return
	}

	// Identity master keys do not contain the "<clustername>" field,
	// Example:
	// K: cilium/state/identities/v1/id/535498
	// V: k8s:io.cilium.k8s.policy.cluster=cluster1;k8s:xxx.pod.namespace=kube-system;
	// So we have to filter them out by ourselves
	if prefix == rc.identityMasterKeyPrefix {
		scopedLog.Info("Filtering identity master entries for remote cluster")
		if alienKVsLeft, err = m.filterIdentityMasterEntries(rc, alienKVsLeft, prefix); err != nil {
			scopedLog.WithError(err).Error("Filter alien identity master entries from local kvstore failed")
			return
		}
	}

	listDoneCh := make(chan bool)
	go func() {
		scopedLog.Info("Waiting for initial list result for GC")

		select {
		case <-listDoneCh:
			log.WithFields(logrus.Fields{
				fieldRemoteClusterName: clusterName,
				fieldPrefix:            prefix,
			}).Info("Initial list done, starting GC alien entries in local kvstore")

			gcStats := m.GC(rc, alienKVsLeft)
			metrics.AlienEntriesGCSize.WithLabelValues(clusterName, prefix, "deleted").Set(float64(gcStats.deleted))
			metrics.AlienEntriesGCSize.WithLabelValues(clusterName, prefix, "alive").Set(float64(gcStats.alive))

			rc.Logger().WithFields(logrus.Fields{
				fieldPrefix:      prefix,
				fieldElapsedTime: gcStats.elapsedTime,
			}).Info("GC done")

		case <-time.After(30 * time.Minute): // TODO: configurable? listTimeoutDefault):
			scopedLog.Errorf("Timeout while initial list remote kvstore")
			return
		}
	}()

	// watcherChanSize is the size of the channel to buffer kvstore events
	watcherChanSize := 100
	watcherName := clusterName + "-" + prefix + "-watcher"

	scopedLog.Info("Going to ListAndWatch remote kvstore")
	watcher := rc.backend.ListAndWatch(ctx, watcherName, prefix, watcherChanSize)

	startTime := time.Now()
	listDone := false
	totalEvents := 0
	inScopeEvents := 0
	inMemoryHandledEvents := 0
	for ev := range watcher.Events {
		if ev.Typ == kvstore.EventTypeListDone {
			scopedLog.WithFields(logrus.Fields{
				"totalEvents":           totalEvents,
				"inScopeEvents":         inScopeEvents,
				"inMemoryHandledEvents": inMemoryHandledEvents,
				fieldElapsedTime:        time.Since(startTime),
			}).Infof("Initial list+sync alien entries done")

			listDone = true
			close(listDoneCh)
			continue
		}

		key := ev.Key
		val := ev.Value
		evType := ev.Typ

		totalEvents++
		metrics.EventsReceivedFromRemoteKVStore.WithLabelValues(clusterName, prefix, evType.String()).Inc()

		evLog := rc.Logger().WithFields(logrus.Fields{
			fieldKey:       key,
			fieldEventType: evType,
		})

		// Validate
		allowProceed, err := m.Validate(
			rc,
			ValidateContext{
				eventType:           evType,
				key:                 key,
				val:                 string(val),
				localKVStoreBackend: localKVStoreBackend,
			},
		)
		if err != nil {
			evLog.WithError(err).Error("Validate remote kvstore event failed")
			continue
		}

		if !allowProceed {
			if listDone {
				evLog.Debug("Validate remote kvstore event: not allowed to proceed")
			}
			continue
		}

		// Process events from the remove kvstore
		switch evType {
		case kvstore.EventTypeModify, kvstore.EventTypeCreate:
			// Determine if alien entry has changed by comparing it with in-memory cache,
			// which significantly reduces the total list+sync time, as most entries should
			// have no changes during kvstoremesh-operator restart/upgrade.
			if !listDone {
				existingVal, ok := alienKVsLeft[key]
				if ok {
					if bytes.Equal(existingVal.Data, val) {
						evLog.Debug("Value of alien entry unchanged, skip apply to local kvstore")
						inMemoryHandledEvents++
					} else {
						evLog.Debug("Value of alien entry changed, apply to local kvstore")
						updateToLocalKVStore(key, val)
					}

					delete(alienKVsLeft, key)
				} else {
					evLog.Debug("Alien entry not found, apply to local kvstore")
					updateToLocalKVStore(key, val)
				}

				inScopeEvents++
				continue
			}

			// Initial List operation finished, entering "mirror" mode:
			// reflect any changes in remote kvstore to local kvstore directly
			updateToLocalKVStore(key, val)

			// Too many update events, so we just log the create events
			if evType == kvstore.EventTypeCreate {
				evLog.Infof("Write to local kvstore successful")
			}
		case kvstore.EventTypeDelete:
			rc.kvstoremesh.kvstoreMutex.Lock()
			if err := localKVStoreBackend.Delete(ctx, key); err != nil {
				evLog.WithError(err).Error("Delete from local kvstore failed")
			}
			rc.kvstoremesh.kvstoreMutex.Unlock()

			if listDone {
				evLog.Infof("Delete from local kvstore successful")
			}
		}

		metrics.EventsAppliedToLocalKVStore.WithLabelValues(clusterName, prefix, evType.String()).Inc()
	}
}
