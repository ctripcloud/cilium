// SPDX-License-Identifier: Apache-2.0
// Copyright 2022 Authors of Cilium

package metrics

import (
	"context"
	"net/http"

	"github.com/cilium/cilium/kvstoremesh-operator/option"
	"github.com/cilium/cilium/pkg/logging"
	"github.com/cilium/cilium/pkg/logging/logfields"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	log = logging.DefaultLogger.WithField(logfields.LogSubsys, "metrics")
)

const Namespace = "kvstoremesh_operator"

var (
	Address string

	Registry   *prometheus.Registry
	shutdownCh chan struct{}
)

func Register() {
	log.Info("Registering metrics")

	Registry = prometheus.NewPedanticRegistry()
	registerMetrics()

	m := http.NewServeMux()
	m.Handle("/metrics", promhttp.HandlerFor(Registry, promhttp.HandlerOpts{}))
	srv := &http.Server{
		Addr:    option.Config.PrometheusServeAddr,
		Handler: m,
	}

	shutdownCh = make(chan struct{})
	go func() {
		go func() {
			err := srv.ListenAndServe()
			switch err {
			case http.ErrServerClosed:
				log.Info("Metrics server shutdown successfully")
				return
			default:
				log.WithError(err).Fatal("Metrics server ListenAndServe failed")
			}
		}()

		<-shutdownCh
		log.Info("Received shutdown signal")
		if err := srv.Shutdown(context.TODO()); err != nil {
			log.WithError(err).Error("Shutdown metrics server failed")
		}
	}()
}

func Unregister() {
	log.Info("Shutting down metrics server")

	if shutdownCh == nil {
		return
	}

	shutdownCh <- struct{}{}
}

var (
	// AlienEntriesGCSize records the alien entries GC results
	AlienEntriesGCSize *prometheus.GaugeVec

	EventsReceivedFromRemoteKVStore *prometheus.CounterVec
	EventsAppliedToLocalKVStore     *prometheus.CounterVec
)

const (
	// LabelStatus marks the status of a resource or completed task
	LabelStatus = "status"

	// LabelOutcome indicates whether the outcome of the operation was successful or not
	LabelOutcome = "outcome"

	// Label values

	// LabelValueOutcomeSuccess is used as a successful outcome of an operation
	LabelValueOutcomeSuccess = "success"

	// LabelValueOutcomeFail is used as an unsuccessful outcome of an operation
	LabelValueOutcomeFail = "fail"
)

func registerMetrics() []prometheus.Collector {
	// Builtin process metrics
	Registry.MustRegister(prometheus.NewProcessCollector(prometheus.ProcessCollectorOpts{Namespace: Namespace}))

	// Custom metrics
	var collectors []prometheus.Collector

	AlienEntriesGCSize = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: Namespace,
		Name:      "alien_entries_deleted",
		Help:      "Number of GC-ed alien entries",
	}, []string{"remoteCluster", "prefix", LabelStatus})
	collectors = append(collectors, AlienEntriesGCSize)

	EventsReceivedFromRemoteKVStore = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: Namespace,
		Name:      "events_received_from_remote_kvstore",
		Help:      "Number of events received from remote kvstore",
	}, []string{"remoteCluster", "prefix", "eventType"})
	collectors = append(collectors, EventsReceivedFromRemoteKVStore)

	EventsAppliedToLocalKVStore = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: Namespace,
		Name:      "events_applied_to_local_kvstore",
		Help:      "Number of events received from remote kvstores and applied to the local kvstore",
	}, []string{"remoteCluster", "prefix", "eventType"})
	collectors = append(collectors, EventsAppliedToLocalKVStore)

	Registry.MustRegister(collectors...)

	return collectors
}
