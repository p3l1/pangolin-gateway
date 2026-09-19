// SPDX-License-Identifier: AGPL-3.0-only

package envtest_test

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/p3l1/pangolin-gateway/test/envtestenv"
)

func TestEnvironmentServesCoreAPI(t *testing.T) {
	c, ctx := envtestenv.Start(t)

	var namespaces corev1.NamespaceList
	if err := c.List(ctx, &namespaces); err != nil {
		t.Fatalf("listing namespaces: %v", err)
	}
	if len(namespaces.Items) == 0 {
		t.Error("no namespaces returned; the API server is not serving core types")
	}
}

// The Gateway API CRDs come from the module cache rather than from a config/crd
// directory this project does not have; this asserts that wiring works.
func TestEnvironmentServesGatewayAPI(t *testing.T) {
	c, ctx := envtestenv.Start(t)

	for _, list := range []client.ObjectList{
		&gatewayv1.HTTPRouteList{},
		&gatewayv1.GatewayList{},
		&gatewayv1.GatewayClassList{},
	} {
		if err := c.List(ctx, list); err != nil {
			t.Errorf("listing %T: %v", list, err)
		}
	}
}
