// SPDX-License-Identifier: AGPL-3.0-only

package controller_test

import (
	"net/http"
	"testing"

	"k8s.io/apimachinery/pkg/types"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/p3l1/pangolin-gateway/internal/gateway"
)

func (h *harness) pinRoute(t *testing.T, name, hostname, pin string) {
	t.Helper()

	r := h.newRoute(name, hostname)
	r.Annotations = map[string]string{gateway.AnnotationPincode: pin}
	h.create(t, r)
}

func (h *harness) lastSent(t *testing.T) (resources, policies map[string]bool) {
	t.Helper()

	bp, ok := h.fake.LastBlueprint()
	if !ok {
		t.Fatal("no blueprint reached the fake")
	}
	resources, policies = map[string]bool{}, map[string]bool{}
	for k := range bp.PublicResources {
		resources[k] = true
	}
	for k := range bp.PublicPolicies {
		policies[k] = true
	}
	return resources, policies
}

// An unchanged PIN-protected entry is left out of the apply. Rationale:
// docs/sessions.md.
func TestReconcileResendsAPinProtectedResourceOnlyWhenItChanged(t *testing.T) {
	h := newHarness(t)
	h.setupClassAndGateway(t, "pangolin")
	h.pinRoute(t, "pin", "pin.example.com", "482913")
	h.create(t, h.newRoute("open", "open.example.com"))

	h.mustReconcile(t)
	if sent, _ := h.lastSent(t); !sent["gw-demo-pin"] || !sent["gw-demo-open"] {
		t.Fatalf("first pass sent %v, want both resources", sent)
	}

	h.mustReconcile(t)
	sent, _ := h.lastSent(t)
	if sent["gw-demo-pin"] {
		t.Error("an unchanged PIN-protected resource was re-sent, ending its sessions")
	}
	if !sent["gw-demo-open"] {
		t.Error("a resource without a PIN was not re-sent; its drift would go unrepaired")
	}
	if _, ok := h.fake.Resources()["gw-demo-pin"]; !ok {
		t.Error("the PIN-protected resource was pruned although it was skipped, not dropped")
	}

	var r gatewayv1.HTTPRoute
	if err := h.client.Get(h.ctx, types.NamespacedName{Namespace: h.namespace, Name: "pin"}, &r); err != nil {
		t.Fatalf("getting route: %v", err)
	}
	r.Annotations[gateway.AnnotationPincode] = "135790"
	if err := h.client.Update(h.ctx, &r); err != nil {
		t.Fatalf("updating route: %v", err)
	}
	h.mustReconcile(t)
	if sent, _ := h.lastSent(t); !sent["gw-demo-pin"] {
		t.Error("a changed PIN was not sent")
	}
}

func TestReconcileRecreatesAPinProtectedResourceDeletedOutOfBand(t *testing.T) {
	h := newHarness(t)
	h.setupClassAndGateway(t, "pangolin")
	h.pinRoute(t, "pin", "pin.example.com", "482913")

	h.mustReconcile(t)
	h.fake.Remove("gw-demo-pin")

	h.mustReconcile(t)
	h.mustReconcile(t)
	if _, ok := h.fake.Resources()["gw-demo-pin"]; !ok {
		t.Error("a PIN-protected resource deleted in Pangolin was never recreated")
	}
}

func TestReconcileResendsAPinProtectedResourceAfterAFailedApply(t *testing.T) {
	h := newHarness(t)
	h.setupClassAndGateway(t, "pangolin")
	h.pinRoute(t, "pin", "pin.example.com", "482913")

	h.fake.FailApply(http.StatusInternalServerError)
	if err := h.reconcile(t); err == nil {
		t.Fatal("reconcile succeeded despite a failing apply")
	}
	h.fake.FailApply(0)

	h.mustReconcile(t)
	if sent, _ := h.lastSent(t); !sent["gw-demo-pin"] {
		t.Error("a resource whose apply failed was treated as applied")
	}
}

func TestReconcileResendsAPinProtectedPolicyOnlyWhenItChanged(t *testing.T) {
	h := newHarness(t)
	h.licensed()
	h.setupClassAndGateway(t, "pangolin")
	h.create(t, h.newPolicy("choir"))
	h.create(t, h.policyRoute("web", "web.example.com", "choir"))

	h.mustReconcile(t)
	if _, sent := h.lastSent(t); !sent["gw-demo-choir"] {
		t.Fatalf("first pass sent policies %v, want gw-demo-choir", sent)
	}

	h.mustReconcile(t)
	if _, sent := h.lastSent(t); sent["gw-demo-choir"] {
		t.Error("an unchanged PIN-protected policy was re-sent, ending its sessions")
	}
	if _, ok := h.fake.Policies()["gw-demo-choir"]; !ok {
		t.Error("the skipped policy was pruned")
	}
}
