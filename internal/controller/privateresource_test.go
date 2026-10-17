// SPDX-License-Identifier: AGPL-3.0-only

package controller_test

import (
	"net/http"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/p3l1/pangolin-gateway/api/v1alpha1"
	"github.com/p3l1/pangolin-gateway/internal/gateway"
	"github.com/p3l1/pangolin-gateway/internal/pangolin"
)

func (h *harness) newHostResource(name string) *v1alpha1.PrivateResource {
	return &v1alpha1.PrivateResource{
		ObjectMeta: metav1.ObjectMeta{Namespace: h.namespace, Name: name},
		Spec: v1alpha1.PrivateResourceSpec{
			DisplayName: "Tekton Webhook",
			Mode:        v1alpha1.ModeHost,
			Destination: name + ".demo.svc.cluster.local",
			TCPPorts:    "8080",
			Alias:       name + ".homelab.internal",
			Machines:    []string{"test-machine"},
		},
	}
}

func (h *harness) newHTTPResource(name, hostname string) *v1alpha1.PrivateResource {
	return &v1alpha1.PrivateResource{
		ObjectMeta: metav1.ObjectMeta{Namespace: h.namespace, Name: name},
		Spec: v1alpha1.PrivateResourceSpec{
			Mode:            v1alpha1.ModeHTTP,
			FullDomain:      hostname,
			Destination:     name + ".demo.svc.cluster.local",
			DestinationPort: 3000,
			Roles:           []string{"Member"},
		},
	}
}

func (h *harness) resource(t *testing.T, name string) v1alpha1.PrivateResource {
	t.Helper()

	var pr v1alpha1.PrivateResource
	if err := h.client.Get(h.ctx, types.NamespacedName{Namespace: h.namespace, Name: name}, &pr); err != nil {
		t.Fatalf("getting PrivateResource %s: %v", name, err)
	}
	return pr
}

func wantCondition(t *testing.T, pr v1alpha1.PrivateResource, typ string, status metav1.ConditionStatus, reason string) metav1.Condition {
	t.Helper()

	c := conditionOf(t, pr.Status.Conditions, typ)
	if c.Status != status || c.Reason != reason {
		t.Errorf("%s = %s/%s (%q), want %s/%s", typ, c.Status, c.Reason, c.Message, status, reason)
	}
	if c.ObservedGeneration != pr.Generation {
		t.Errorf("%s observedGeneration = %d, want %d", typ, c.ObservedGeneration, pr.Generation)
	}
	return c
}

func TestReconcilePublishesAPrivateResourceAndSetsStatus(t *testing.T) {
	h := newHarness(t)
	h.create(t, h.newHostResource("tekton"))
	h.create(t, h.newHTTPResource("mcp", "mcp.example.com"))

	h.mustReconcile(t)

	held := h.fake.PrivateResources()
	host, ok := held["gw-pr-demo-tekton"]
	if !ok {
		t.Fatalf("host resource not published; fake holds %v", held)
	}
	if host.Mode != pangolin.ModeHost || host.Alias != "tekton.homelab.internal" ||
		host.TCPPorts == nil || *host.TCPPorts != "8080" || host.UDPPorts == nil || *host.UDPPorts != "" {
		t.Errorf("host entry = %+v", host)
	}
	if len(host.Sites) != 1 || host.Sites[0] != "test-site" {
		t.Errorf("sites = %v, want the default site", host.Sites)
	}
	if len(host.Machines) != 1 || host.Machines[0] != "test-machine" {
		t.Errorf("machines = %v, want [test-machine]", host.Machines)
	}
	web, ok := held["gw-pr-demo-mcp"]
	if !ok || web.FullDomain != "mcp.example.com" || web.SSL == nil || !*web.SSL || web.DestinationPort != 3000 {
		t.Errorf("http entry = %+v (present %t)", web, ok)
	}

	for name, key := range map[string]string{"tekton": "gw-pr-demo-tekton", "mcp": "gw-pr-demo-mcp"} {
		pr := h.resource(t, name)
		wantCondition(t, pr, v1alpha1.ConditionAccepted, metav1.ConditionTrue, gateway.ReasonAccepted)
		wantCondition(t, pr, v1alpha1.ConditionProgrammed, metav1.ConditionTrue, gateway.ReasonProgrammed)
		if pr.Status.ResourceKey != key {
			t.Errorf("%s: resourceKey = %q, want %q", name, pr.Status.ResourceKey, key)
		}
	}
}

func TestReconcilePrunesAPrivateResourceThatIsGone(t *testing.T) {
	h := newHarness(t)
	pr := h.newHostResource("tekton")
	h.create(t, pr)
	h.mustReconcile(t)
	if _, ok := h.fake.PrivateResources()["gw-pr-demo-tekton"]; !ok {
		t.Fatal("not published")
	}

	if err := h.client.Delete(h.ctx, pr); err != nil {
		t.Fatalf("deleting PrivateResource: %v", err)
	}
	h.mustReconcile(t)

	if _, ok := h.fake.PrivateResources()["gw-pr-demo-tekton"]; ok {
		t.Error("the resource survived its PrivateResource")
	}
	if got := h.fake.PrivateDeleted(); len(got) != 1 {
		t.Errorf("privateDeleted = %v, want one delete through the private endpoint", got)
	}
}

// One invalid resource must not take the others down with it, which is what
// Pangolin would do if the document reached it.
func TestReconcileRejectsOnlyTheInvalidPrivateResource(t *testing.T) {
	h := newHarness(t)
	h.setupClassAndGateway(t, "pangolin")
	h.create(t, h.newRoute("web", "web.example.com"))
	h.create(t, h.newHostResource("good"))
	bad := h.newHostResource("bad")
	bad.Spec.TCPPorts = "9000-8000"
	h.create(t, bad)

	h.mustReconcile(t)

	if _, ok := h.fake.PrivateResources()["gw-pr-demo-good"]; !ok {
		t.Error("the valid resource was not published")
	}
	if _, ok := h.fake.Resources()["gw-demo-web"]; !ok {
		t.Error("the route was not published")
	}
	if _, ok := h.fake.PrivateResources()["gw-pr-demo-bad"]; ok {
		t.Error("the invalid resource was published")
	}

	pr := h.resource(t, "bad")
	c := wantCondition(t, pr, v1alpha1.ConditionAccepted, metav1.ConditionFalse, gateway.ReasonInvalidSpec)
	if !strings.Contains(c.Message, "tcpPorts") {
		t.Errorf("message = %q, want it to name tcpPorts", c.Message)
	}
	wantCondition(t, pr, v1alpha1.ConditionProgrammed, metav1.ConditionFalse, gateway.ReasonNotAccepted)
	if pr.Status.ResourceKey != "" {
		t.Errorf("resourceKey = %q, want none for a rejected resource", pr.Status.ResourceKey)
	}
}

func TestReconcileChecksMachinesUnlessTheyCannotBeListed(t *testing.T) {
	h := newHarness(t)
	pr := h.newHostResource("tekton")
	pr.Spec.Machines = []string{"test-machine", "fine-mastigoproctus-giganteus"}
	h.create(t, pr)

	h.mustReconcile(t)
	got := h.resource(t, "tekton")
	c := wantCondition(t, got, v1alpha1.ConditionAccepted, metav1.ConditionFalse, gateway.ReasonUnknownMachine)
	if !strings.Contains(c.Message, "fine-mastigoproctus-giganteus") {
		t.Errorf("message = %q, want it to name the machine", c.Message)
	}

	// A key without listClients degrades to Pangolin's own resolution.
	h.fake.FailMachines(http.StatusForbidden)
	h.mustReconcile(t)
	if _, ok := h.fake.PrivateResources()["gw-pr-demo-tekton"]; !ok {
		t.Error("not published once the machine listing was unavailable")
	}
	wantCondition(t, h.resource(t, "tekton"), v1alpha1.ConditionAccepted,
		metav1.ConditionTrue, gateway.ReasonAccepted)
}

func TestReconcileRejectsAPrivateResourceWithAnUnknownUser(t *testing.T) {
	h := newHarness(t)
	pr := h.newHTTPResource("mcp", "mcp.example.com")
	pr.Spec.Users = []string{"test-user@example.com", "ghost"}
	h.create(t, pr)

	h.mustReconcile(t)

	wantCondition(t, h.resource(t, "mcp"), v1alpha1.ConditionAccepted,
		metav1.ConditionFalse, gateway.ReasonUnknownUser)
}

// A failed apply leaves the spec accepted but the resource not programmed.
func TestReconcileReportsAPublishFailureOnThePrivateResource(t *testing.T) {
	h := newHarness(t)
	h.create(t, h.newHostResource("tekton"))
	h.fake.FailApply(http.StatusBadRequest)

	if err := h.reconcile(t); err == nil {
		t.Fatal("the failed apply was swallowed")
	}

	pr := h.resource(t, "tekton")
	wantCondition(t, pr, v1alpha1.ConditionAccepted, metav1.ConditionTrue, gateway.ReasonAccepted)
	wantCondition(t, pr, v1alpha1.ConditionProgrammed, metav1.ConditionFalse, gateway.ReasonPublishFailed)
}

func TestReconcileMarksAPrivateResourceAsDryRun(t *testing.T) {
	h := newHarness(t)
	h.publisher.DryRun = true
	h.create(t, h.newHostResource("tekton"))

	h.mustReconcile(t)

	wantCondition(t, h.resource(t, "tekton"), v1alpha1.ConditionProgrammed,
		metav1.ConditionFalse, gateway.ReasonDryRun)
}

// A status write is a watch event, so an unchanged pass must not write again.
func TestReconcileStopsWritingPrivateResourceStatusOnceSettled(t *testing.T) {
	h := newHarness(t)
	h.create(t, h.newHostResource("tekton"))

	h.mustReconcile(t)
	before := h.resource(t, "tekton").ResourceVersion
	h.mustReconcile(t)

	if after := h.resource(t, "tekton").ResourceVersion; after != before {
		t.Errorf("resourceVersion moved from %s to %s on a pass that changed nothing", before, after)
	}
}

func TestReconcileResolvesAHostnameCollisionWithAPrivateRoute(t *testing.T) {
	h := newHarness(t)
	h.setupClassAndGateway(t, "pangolin")
	h.create(t, h.newPrivateRoute("docs", "docs.example.com"))
	h.mustReconcile(t)

	h.create(t, h.newHTTPResource("wiki", "docs.example.com"))
	h.mustReconcile(t)

	if _, ok := h.fake.PrivateResources()["gw-demo-docs"]; !ok {
		t.Error("the older route lost its hostname")
	}
	if _, ok := h.fake.PrivateResources()["gw-pr-demo-wiki"]; ok {
		t.Error("the newer PrivateResource was published under a claimed hostname")
	}
	c := wantCondition(t, h.resource(t, "wiki"), v1alpha1.ConditionAccepted,
		metav1.ConditionFalse, gateway.ReasonDuplicateHostname)
	if !strings.Contains(c.Message, "HTTPRoute demo/docs") {
		t.Errorf("message = %q, want it to name the owning route", c.Message)
	}
}

// The schema rejects what is wrong in shape at admission, before the
// controller ever sees it.
func TestTheCRDRejectsAnUnknownMode(t *testing.T) {
	h := newHarness(t)
	pr := h.newHostResource("tekton")
	pr.Spec.Mode = "tcp"

	err := h.client.Create(h.ctx, pr)
	if err == nil || !strings.Contains(err.Error(), "spec.mode") {
		t.Errorf("create err = %v, want a validation error on spec.mode", err)
	}
}
