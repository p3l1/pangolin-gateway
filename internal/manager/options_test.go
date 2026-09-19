// SPDX-License-Identifier: AGPL-3.0-only

package manager

import (
	"testing"

	"k8s.io/apimachinery/pkg/runtime/schema"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

func TestControllerOptionsPassesAddressesThrough(t *testing.T) {
	opts := ControllerOptions(Config{
		MetricsAddress: ":9090",
		ProbeAddress:   ":9091",
		LeaderElection: true,
	})

	if got, want := opts.Metrics.BindAddress, ":9090"; got != want {
		t.Errorf("metrics address = %q, want %q", got, want)
	}
	if got, want := opts.HealthProbeBindAddress, ":9091"; got != want {
		t.Errorf("probe address = %q, want %q", got, want)
	}
	if !opts.LeaderElection {
		t.Error("leader election disabled, want enabled")
	}
}

func TestControllerOptionsUsesStableLeaderElectionID(t *testing.T) {
	opts := ControllerOptions(Config{})

	if got, want := opts.LeaderElectionID, LeaderElectionID; got != want {
		t.Errorf("lock name = %q, want %q", got, want)
	}
	if want := "pangolin-gateway.p3l1.de"; LeaderElectionID != want {
		t.Errorf("LeaderElectionID = %q, want %q", LeaderElectionID, want)
	}
}

func TestControllerOptionsSchemeKnowsGatewayTypes(t *testing.T) {
	opts := ControllerOptions(Config{})

	gv := schema.GroupVersion{Group: gatewayv1.GroupName, Version: "v1"}
	for _, kind := range []string{"HTTPRoute", "Gateway", "GatewayClass"} {
		if !opts.Scheme.Recognizes(gv.WithKind(kind)) {
			t.Errorf("scheme does not recognise gateway.networking.k8s.io %s", kind)
		}
	}
}
