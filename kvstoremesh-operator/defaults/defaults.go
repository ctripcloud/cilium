// SPDX-License-Identifier: Apache-2.0
// Copyright 2022 Authors of Cilium

package defaults

import (
	"time"
)

const (
	ClusterName = "default"

	// RemoteKVStoresConfigDir is the default directory holding configuration
	// files of remote kvstores
	RemoteKVStoresConfigDir = "/var/lib/kvstoremesh"

	// EnableIPv4 is the default value for IPv4 enablement
	EnableIPv4 = true

	// EnableIPv6 is the default value for IPv6 enablement
	EnableIPv6 = false

	// KVStoreStaleLockTimeout is the timeout for when a lock is held for
	// a kvstore path for too long.
	KVStoreStaleLockTimeout = 30 * time.Second

	// KVstoreQPS is default rate limit for kv store operations
	KVstoreQPS = 20

	// KVstoreMaxConsecutiveQuorumErrors is the maximum number of acceptable
	// kvstore consecutive quorum errors before the agent assumes permanent failure
	KVstoreMaxConsecutiveQuorumErrors = 2

	// AllocatorListTimeout specifies the standard time to allow for listing
	// initial allocator state from kvstore before exiting.
	AllocatorListTimeout = 30 * time.Minute

	// KVstoreLeaseTTL is the time-to-live of the kvstore lease.
	KVstoreLeaseTTL = 15 * time.Minute

	// KVstoreKeepAliveIntervalFactor is the factor to calculate the interval
	// from KVstoreLeaseTTL in which KVstore lease is being renewed.
	KVstoreKeepAliveIntervalFactor = 3

	// LockLeaseTTL is the time-to-live of the lease dedicated for locks of Kvstore.
	LockLeaseTTL = 25 * time.Second

	// KVstoreLeaseMaxTTL is the upper bound for KVStore lease TTL value.
	// It is calculated as Min(int64 positive max, etcd MaxLeaseTTL, consul MaxLeaseTTL)
	KVstoreLeaseMaxTTL = 240 * time.Hour

	// K8sSyncTimeout specifies the standard time to allow for synchronizing
	// local caches with Kubernetes state before exiting.
	K8sSyncTimeout = 3 * time.Minute

	// K8sClientQPSLimit is the default qps for the k8s client. It is set to 0 because the the k8s client
	// has its own default.
	K8sClientQPSLimit float64 = 0.0

	// K8sClientBurst is the default burst for the k8s client. It is set to 0 because the the k8s client
	// has its own default.
	K8sClientBurst = 0

	// APIServeAddr is the default server address for metrics
	APIServeAddr = ":9235"

	// PrometheusServeAddr is the default server address for operator metrics
	PrometheusServeAddr = ":6943"

	// GopsPort is the default value for option.GopsPort in the kvstoremesh-operator
	GopsPort = 9894
)
