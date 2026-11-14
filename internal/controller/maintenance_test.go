// SPDX-License-Identifier: AGPL-3.0-only

package controller_test

import (
	"testing"

	"k8s.io/apimachinery/pkg/types"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/p3l1/pangolin-gateway/internal/gateway"
)

func TestReconcileSwitchesTheMaintenancePageOnAndOff(t *testing.T) {
	h := newHarness(t)
	h.licensed()
	h.setupClassAndGateway(t, "pangolin")
	r := h.newRoute("web", "web.example.com")
	r.Annotations = map[string]string{
		gateway.AnnotationMaintenanceMode:    gateway.MaintenanceForced,
		gateway.AnnotationMaintenanceTitle:   "Kurze Pause",
		gateway.AnnotationMaintenanceMessage: "Bitte warten.",
	}
	h.create(t, r)
	h.mustReconcile(t)

	m := h.fake.Resources()["gw-demo-web"].Maintenance
	if m == nil || !m.Enabled || m.Type != gateway.MaintenanceForced || m.Title != "Kurze Pause" {
		t.Fatalf("maintenance = %+v, want the forced page", m)
	}

	var live gatewayv1.HTTPRoute
	if err := h.client.Get(h.ctx, types.NamespacedName{Namespace: h.namespace, Name: "web"}, &live); err != nil {
		t.Fatalf("getting route: %v", err)
	}
	delete(live.Annotations, gateway.AnnotationMaintenanceMode)
	if err := h.client.Update(h.ctx, &live); err != nil {
		t.Fatalf("updating route: %v", err)
	}
	h.mustReconcile(t)

	if m := h.fake.Resources()["gw-demo-web"].Maintenance; m == nil || m.Enabled {
		t.Errorf("maintenance = %+v, want it switched off", m)
	}
}
