// SPDX-License-Identifier: AGPL-3.0-only

// Package envtestenv boots a real Kubernetes API server for medium-tier tests, so both
// test/envtest and internal/controller can share one implementation of the harness.
package envtestenv

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	apiruntime "k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/p3l1/pangolin-gateway/api/v1alpha1"
)

// CRDPathEnv names the directory holding the Gateway API CRDs. `just test`
// resolves them from the module cache, so the schemas the tests run against
// are the ones go.mod pins.
const CRDPathEnv = "GATEWAY_API_CRDS"

// projectCRDs is this project's own generated CRDs, found relative to this file
// so the harness works from whichever package directory go test runs it in.
func projectCRDs() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "config", "crd", "bases")
}

// Start boots an API server with the Gateway API CRDs and this project's own,
// and stops it with the test. addToScheme registers extra API types on top of
// the client-go, Gateway API and pangolin.p3l1.de types, which are always
// present. The client is WithWatch (a superset of Client) so callers can wrap it.
func Start(t *testing.T, addToScheme ...func(*apiruntime.Scheme) error) (client.WithWatch, context.Context) {
	t.Helper()

	crds := os.Getenv(CRDPathEnv)
	if crds == "" {
		t.Fatalf("%s is unset (run: just test)", CRDPathEnv)
	}

	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{crds, projectCRDs()},
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

	scheme := apiruntime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("registering client-go scheme: %v", err)
	}
	if err := gatewayv1.Install(scheme); err != nil {
		t.Fatalf("registering gateway-api scheme: %v", err)
	}
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("registering pangolin.p3l1.de scheme: %v", err)
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
