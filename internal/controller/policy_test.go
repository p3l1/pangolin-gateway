// SPDX-License-Identifier: AGPL-3.0-only

package controller_test

import (
	"net/http"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/p3l1/pangolin-gateway/api/v1alpha1"
	"github.com/p3l1/pangolin-gateway/internal/gateway"
	"github.com/p3l1/pangolin-gateway/internal/pangolin"
)

// licensed puts the controller and the fake Pangolin on a licensed footing.
func (h *harness) licensed() {
	h.publisher.Licensed = true
	h.fake.SetLicensed(true)
}

func (h *harness) newPolicy(name string) *v1alpha1.ResourcePolicy {
	return &v1alpha1.ResourcePolicy{
		ObjectMeta: metav1.ObjectMeta{Namespace: h.namespace, Name: name},
		Spec: v1alpha1.ResourcePolicySpec{
			DisplayName:    "Choir",
			EmailWhitelist: []string{"anna@example.com", "*@plan-b-chor.de"},
			Pincode:        "012345",
		},
	}
}

func (h *harness) policy(t *testing.T, name string) v1alpha1.ResourcePolicy {
	t.Helper()

	var rp v1alpha1.ResourcePolicy
	if err := h.client.Get(h.ctx, types.NamespacedName{Namespace: h.namespace, Name: name}, &rp); err != nil {
		t.Fatalf("getting ResourcePolicy %s: %v", name, err)
	}
	return rp
}

func (h *harness) policyRoute(name, hostname, policy string) *gatewayv1.HTTPRoute {
	r := h.newRoute(name, hostname)
	r.Annotations = map[string]string{gateway.AnnotationPolicy: policy}
	return r
}

func TestReconcilePublishesAPolicyAndTheRouteReferencingIt(t *testing.T) {
	h := newHarness(t)
	h.licensed()
	h.setupClassAndGateway(t, "pangolin")
	h.create(t, h.newPolicy("choir"))
	h.create(t, h.policyRoute("web", "web.example.com", "choir"))

	h.mustReconcile(t)

	held := h.fake.Policies()["gw-demo-choir"]
	if held.Name != "Choir" || held.Pincode != "012345" || !held.EmailWhitelistEnabled ||
		len(held.WhitelistUsers) != 2 || held.WhitelistUsers[1] != "*@plan-b-chor.de" {
		t.Errorf("policy = %+v", held)
	}
	if got := h.fake.Resources()["gw-demo-web"].Policy; got != "gw-demo-choir" {
		t.Errorf("route policy = %q, want gw-demo-choir", got)
	}

	rp := h.policy(t, "choir")
	for typ, reason := range map[string]string{
		v1alpha1.ConditionAccepted:   gateway.ReasonAccepted,
		v1alpha1.ConditionProgrammed: gateway.ReasonProgrammed,
	} {
		c := conditionOf(t, rp.Status.Conditions, typ)
		if c.Status != metav1.ConditionTrue || c.Reason != reason || c.ObservedGeneration != rp.Generation {
			t.Errorf("%s = %s/%s (%q), want True/%s", typ, c.Status, c.Reason, c.Message, reason)
		}
	}
	if rp.Status.PolicyKey != "gw-demo-choir" {
		t.Errorf("policyKey = %q, want gw-demo-choir", rp.Status.PolicyKey)
	}

	accepted := conditionOf(t, h.routeConditions(t, "web"), "Accepted")
	if accepted.Status != metav1.ConditionTrue {
		t.Errorf("route Accepted = %s (%s)", accepted.Status, accepted.Message)
	}
}

// Pangolin refuses to delete a policy a resource still names, so the prune
// has to run after the apply has moved the route to its new policy.
func TestReconcilePrunesAPolicyOnceNoRouteNamesIt(t *testing.T) {
	h := newHarness(t)
	h.licensed()
	h.setupClassAndGateway(t, "pangolin")
	choir := h.newPolicy("choir")
	h.create(t, choir)
	h.create(t, h.newPolicy("band"))
	h.create(t, h.policyRoute("web", "web.example.com", "choir"))
	h.mustReconcile(t)

	var route gatewayv1.HTTPRoute
	if err := h.client.Get(h.ctx, types.NamespacedName{Namespace: h.namespace, Name: "web"}, &route); err != nil {
		t.Fatalf("getting route: %v", err)
	}
	route.Annotations[gateway.AnnotationPolicy] = "band"
	if err := h.client.Update(h.ctx, &route); err != nil {
		t.Fatalf("updating route: %v", err)
	}
	if err := h.client.Delete(h.ctx, choir); err != nil {
		t.Fatalf("deleting ResourcePolicy: %v", err)
	}
	h.mustReconcile(t)

	policies := h.fake.Policies()
	if _, ok := policies["gw-demo-choir"]; ok {
		t.Error("the policy survived its ResourcePolicy")
	}
	if _, ok := policies["gw-demo-band"]; !ok {
		t.Error("the policy still in use was pruned")
	}
	if got := h.fake.Resources()["gw-demo-web"].Policy; got != "gw-demo-band" {
		t.Errorf("route policy = %q, want gw-demo-band", got)
	}
	if len(h.fake.PolicyDeleted()) != 1 {
		t.Errorf("policyDeleted = %v, want one delete", h.fake.PolicyDeleted())
	}
}

func TestReconcileLeavesForeignPoliciesAlone(t *testing.T) {
	h := newHarness(t)
	h.licensed()
	h.fake.SeedPolicy("handwritten", pangolin.PublicPolicy{Name: "Hand", SSO: true})

	h.mustReconcile(t)

	if _, ok := h.fake.Policies()["handwritten"]; !ok {
		t.Error("the prune deleted a policy outside the gw- prefix")
	}
}

// A refused DELETE (the policy is still in use by something this controller
// did not apply) is retried and does not fail the pass.
func TestReconcileRetriesAFailedPolicyDeleteWithoutFailingThePass(t *testing.T) {
	h := newHarness(t)
	h.licensed()
	h.setupClassAndGateway(t, "pangolin")
	h.fake.SeedPolicy("gw-demo-orphan", pangolin.PublicPolicy{Name: "Orphan", SSO: true})
	h.create(t, h.newRoute("web", "web.example.com"))

	h.fake.FailPolicyDelete(http.StatusConflict)
	h.mustReconcile(t)

	if _, ok := h.fake.Resources()["gw-demo-web"]; !ok {
		t.Error("a refused policy delete prevented the apply")
	}
	if _, ok := h.fake.Policies()["gw-demo-orphan"]; !ok {
		t.Fatal("the orphan vanished despite the delete being refused")
	}

	h.fake.FailPolicyDelete(0)
	h.mustReconcile(t)

	if _, ok := h.fake.Policies()["gw-demo-orphan"]; ok {
		t.Error("the orphan was not retried on the next pass")
	}
}

func TestReconcileSkipsThePolicyPruneWhenTheListingFails(t *testing.T) {
	h := newHarness(t)
	h.licensed()
	h.fake.SeedPolicy("gw-demo-orphan", pangolin.PublicPolicy{Name: "Orphan", SSO: true})

	h.fake.FailPolicyList(http.StatusInternalServerError)
	if err := h.reconcile(t); err == nil {
		t.Fatal("reconcile hid a failing policy listing")
	}
	if len(h.fake.PolicyDeleted()) != 0 {
		t.Error("the prune deleted on a listing that failed")
	}
}

// Without a licence the policy routes answer 403, so the controller must not
// call them at all: a failing listing would otherwise fail every pass.
func TestReconcileNeverCallsThePolicyAPIWhileUnlicensed(t *testing.T) {
	h := newHarness(t)
	h.setupClassAndGateway(t, "pangolin")
	h.create(t, h.newPolicy("choir"))
	h.create(t, h.newRoute("web", "web.example.com"))
	h.create(t, h.policyRoute("fancy", "fancy.example.com", "choir"))

	h.mustReconcile(t)

	bp, _ := h.fake.LastBlueprint()
	if len(bp.PublicPolicies) != 0 {
		t.Errorf("public-policies = %v, want none", bp.PublicPolicies)
	}
	if _, ok := h.fake.Resources()["gw-demo-web"]; !ok {
		t.Error("the plain route was not published")
	}
	if _, ok := h.fake.Resources()["gw-demo-fancy"]; ok {
		t.Error("the route referencing a policy was published")
	}

	rp := h.policy(t, "choir")
	c := conditionOf(t, rp.Status.Conditions, v1alpha1.ConditionAccepted)
	if c.Status != metav1.ConditionFalse || c.Reason != gateway.ReasonNotLicensed ||
		!strings.Contains(c.Message, "not configured as licensed") {
		t.Errorf("Accepted = %s/%s (%q)", c.Status, c.Reason, c.Message)
	}
	if rp.Status.PolicyKey != "" {
		t.Errorf("policyKey = %q, want empty", rp.Status.PolicyKey)
	}

	fancy := conditionOf(t, h.routeConditions(t, "fancy"), "Accepted")
	if fancy.Status != metav1.ConditionFalse || fancy.Reason != "UnsupportedValue" {
		t.Errorf("route Accepted = %s/%s (%q)", fancy.Status, fancy.Reason, fancy.Message)
	}
}

func TestReconcilePublishesTheInlineEmailWhitelist(t *testing.T) {
	h := newHarness(t)
	h.setupClassAndGateway(t, "pangolin")
	r := h.newRoute("web", "web.example.com")
	r.Annotations = map[string]string{gateway.AnnotationEmailWhitelist: "Anna@example.com, bob@example.org"}
	h.create(t, r)

	h.mustReconcile(t)

	auth := h.fake.Resources()["gw-demo-web"].Auth
	if auth == nil || len(auth.WhitelistUsers) != 2 || auth.WhitelistUsers[0] != "anna@example.com" {
		t.Errorf("auth = %+v", auth)
	}
}

func TestReconcileRejectsOnlyTheRouteWithAWildcardWhitelist(t *testing.T) {
	h := newHarness(t)
	h.setupClassAndGateway(t, "pangolin")
	h.create(t, h.newRoute("good", "good.example.com"))
	bad := h.newRoute("bad", "bad.example.com")
	bad.Annotations = map[string]string{gateway.AnnotationEmailWhitelist: "*@example.com"}
	h.create(t, bad)

	h.mustReconcile(t)

	if _, ok := h.fake.Resources()["gw-demo-good"]; !ok {
		t.Error("one bad whitelist took the good route down")
	}
	if _, ok := h.fake.Resources()["gw-demo-bad"]; ok {
		t.Error("the wildcard whitelist reached Pangolin")
	}
}
