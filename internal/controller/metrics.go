// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	publishTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "pangolin_gateway_publish_total",
		Help: "Blueprint applies, by result.",
	}, []string{"result"})

	pruneTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "pangolin_gateway_prune_total",
		Help: "Individual resource deletions attempted by the prune, by result.",
	}, []string{"result"})

	pruneSkippedTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "pangolin_gateway_prune_skipped_total",
		Help: "Passes in which the prune did not run, by reason.",
	}, []string{"reason"})

	resourcesDesired = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "pangolin_gateway_resources_desired",
		Help: "Resources in the most recently rendered blueprint.",
	})

	routesRejected = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "pangolin_gateway_routes_rejected",
		Help: "Served routes the renderer refused to publish.",
	})
)

func init() {
	metrics.Registry.MustRegister(
		publishTotal, pruneTotal, pruneSkippedTotal, resourcesDesired, routesRejected,
	)
}
