// SPDX-License-Identifier: AGPL-3.0-only

// Package envtestenv boots a real Kubernetes API server for medium-tier tests, so both
// test/envtest and internal/controller can share one implementation of the harness.
package envtestenv

import (
	"context"
	"os"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// CRDPathEnv names the directory holding the Gateway API CRDs. This project
// defines none of its own, so `just test` resolves them from the module cache —
// the schemas the tests run against are then the ones go.mod pins.
const CRDPathEnv = "GATEWAY_API_CRDS"

// Start boots an API server with the Gateway API CRDs and stops it with the test.
// addToScheme registers extra API types on top of the built-in client-go scheme
// and the Gateway API types, which are always present. The returned client is
// WithWatch (a superset of Client) so callers that need to wrap it can.
func Start(t *testing.T, addToScheme ...func(*runtime.Scheme) error) (client.WithWatch, context.Context) {
	t.Helper()

	crds := os.Getenv(CRDPathEnv)
	if crds == "" {
		t.Fatalf("%s is unset (run: just test)", CRDPathEnv)
	}

	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{crds},
		ErrorIfCRDPathMissing: true,
	}

	cfg, err := env.Start()
	if err != nil {
		t.Fatalf("starting envtest: %v (run: just test)", err)
	}
	t.Cleanup(func() {
		if err := env.Stop(); err != nil {
			t.Errorf("stopping envtest: %v", err)
		}
	})

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("registering client-go scheme: %v", err)
	}
	if err := gatewayv1.Install(scheme); err != nil {
		t.Fatalf("registering gateway-api scheme: %v", err)
	}
	for _, add := range addToScheme {
		if err := add(scheme); err != nil {
			t.Fatalf("registering scheme: %v", err)
		}
	}

	c, err := client.NewWithWatch(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatalf("building client: %v", err)
	}
	return c, context.Background()
}
