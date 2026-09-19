// SPDX-License-Identifier: AGPL-3.0-only

// Package manager assembles the controller-runtime manager configuration.
package manager

import (
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// Changing this splits an upgrading controller into two active leaders.
const LeaderElectionID = "pangolin-gateway.p3l1.de"

// ControllerName identifies the GatewayClasses this controller serves.
const ControllerName = "p3l1.de/pangolin"

type Config struct {
	MetricsAddress string
	ProbeAddress   string
	LeaderElection bool
}

func ControllerOptions(c Config) ctrl.Options {
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(gatewayv1.Install(scheme))

	return ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: c.MetricsAddress},
		HealthProbeBindAddress: c.ProbeAddress,
		LeaderElection:         c.LeaderElection,
		LeaderElectionID:       LeaderElectionID,
	}
}
