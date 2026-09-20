// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

// Label values for the blueprint section a metric is about. Public and private
// resources are separate listings with separate deletions, so a total that
// merged them would hide one section failing while the other worked.
const (
	visibilityPublic  = "public"
	visibilityPrivate = "private"
)

var (
	publishTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "pangolin_gateway_publish_total",
		Help: "Blueprint applies, by result.",
	}, []string{"result"})

	pruneTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "pangolin_gateway_prune_total",
		Help: "Individual resource deletions attempted by the prune, by result.",
	}, []string{"result", "visibility"})

	pruneSkippedTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "pangolin_gateway_prune_skipped_total",
		Help: "Passes in which the prune did not run, by reason.",
	}, []string{"reason", "visibility"})

	resourcesDesired = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "pangolin_gateway_resources_desired",
		Help: "Resources in the most recently rendered blueprint.",
	}, []string{"visibility"})

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
