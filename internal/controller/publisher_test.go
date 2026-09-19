// SPDX-License-Identifier: AGPL-3.0-only

package controller_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/p3l1/pangolin-gateway/internal/controller"
	"github.com/p3l1/pangolin-gateway/internal/gateway"
	"github.com/p3l1/pangolin-gateway/internal/pangolin"
	"github.com/p3l1/pangolin-gateway/test/envtestenv"
	"github.com/p3l1/pangolin-gateway/test/pangolinfake"
)

const controllerName = "p3l1.de/pangolin"

func ptr[T any](v T) *T { return &v }

type harness struct {
	client    client.WithWatch
	fake      *pangolinfake.Fake
	publisher *controller.Publisher
	ctx       context.Context
	namespace string
}

func newHarness(t *testing.T) *harness {
	t.Helper()

	c, ctx := envtestenv.Start(t)

	f := pangolinfake.New()
	srv := httptest.NewServer(f.Handler())
	t.Cleanup(srv.Close)

	pc, err := pangolin.NewHTTPClient(pangolin.Options{
		Endpoint: srv.URL + "/v1",
		OrgID:    "test-org",
		APIKey:   "id.secret",
	})
	if err != nil {
		t.Fatalf("building pangolin client: %v", err)
	}

	h := &harness{
		client: c,
		fake:   f,
		publisher: &controller.Publisher{
			Client:         c,
			Pangolin:       pc,
			ControllerName: controllerName,
			DefaultSite:    "test-site",
		},
		ctx:       ctx,
		namespace: "demo",
	}
	h.create(t, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: h.namespace}})
	return h
}

func (h *harness) create(t *testing.T, obj client.Object) {
	t.Helper()

	if err := h.client.Create(h.ctx, obj); err != nil {
		t.Fatalf("creating %T %s: %v", obj, obj.GetName(), err)
	}
}

func (h *harness) reconcile(t *testing.T) error {
	t.Helper()

	_, err := h.publisher.Reconcile(ctrl.LoggerInto(h.ctx, ctrl.Log), ctrl.Request{})
	return err
}

func (h *harness) mustReconcile(t *testing.T) {
	t.Helper()

	if err := h.reconcile(t); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
}

func (h *harness) setupClassAndGateway(t *testing.T, className string) {
	t.Helper()

	h.create(t, &gatewayv1.GatewayClass{
		ObjectMeta: metav1.ObjectMeta{Name: className},
		Spec:       gatewayv1.GatewayClassSpec{ControllerName: controllerName},
	})
	h.create(t, &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Namespace: h.namespace, Name: "gw"},
		Spec: gatewayv1.GatewaySpec{
			GatewayClassName: gatewayv1.ObjectName(className),
			Listeners: []gatewayv1.Listener{{
				Name:     "http",
				Port:     80,
				Protocol: gatewayv1.HTTPProtocolType,
			}},
		},
	})
}

func (h *harness) newRoute(name, hostname string) *gatewayv1.HTTPRoute {
	return &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Namespace: h.namespace, Name: name},
		Spec: gatewayv1.HTTPRouteSpec{
			CommonRouteSpec: gatewayv1.CommonRouteSpec{
				ParentRefs: []gatewayv1.ParentReference{{Name: "gw"}},
			},
			Hostnames: []gatewayv1.Hostname{gatewayv1.Hostname(hostname)},
			Rules: []gatewayv1.HTTPRouteRule{{
				BackendRefs: []gatewayv1.HTTPBackendRef{{
					BackendRef: gatewayv1.BackendRef{
						BackendObjectReference: gatewayv1.BackendObjectReference{
							Name: gatewayv1.ObjectName(name),
							Port: ptr(gatewayv1.PortNumber(8080)),
						},
					},
				}},
			}},
		},
	}
}

func (h *harness) routeConditions(t *testing.T, name string) []metav1.Condition {
	t.Helper()

	var r gatewayv1.HTTPRoute
	key := types.NamespacedName{Namespace: h.namespace, Name: name}
	if err := h.client.Get(h.ctx, key, &r); err != nil {
		t.Fatalf("getting route %s: %v", name, err)
	}
	for _, ps := range r.Status.Parents {
		if string(ps.ControllerName) == controllerName {
			return ps.Conditions
		}
	}
	t.Fatalf("no parent entry for %s on route %s; have %+v", controllerName, name, r.Status.Parents)
	return nil
}

func conditionOf(t *testing.T, conds []metav1.Condition, typ string) metav1.Condition {
	t.Helper()

	for _, c := range conds {
		if c.Type == typ {
			return c
		}
	}
	t.Fatalf("no %s condition; have %+v", typ, conds)
	return metav1.Condition{}
}

func TestReconcilePublishesARouteAndSetsStatus(t *testing.T) {
	h := newHarness(t)
	h.setupClassAndGateway(t, "pangolin")
	h.create(t, h.newRoute("web", "web.example.com"))

	h.mustReconcile(t)

	bp, ok := h.fake.LastBlueprint()
	if !ok {
		t.Fatal("no blueprint reached the fake")
	}
	entry, ok := bp.PublicResources["gw-demo-web"]
	if !ok {
		t.Fatalf("no gw-demo-web in the blueprint; have %v", bp.PublicResources)
	}
	if got, want := entry.FullDomain, "web.example.com"; got != want {
		t.Errorf("full-domain = %q, want %q", got, want)
	}
	if got, want := entry.Targets[0].Site, "test-site"; got != want {
		t.Errorf("site = %q, want the default %q", got, want)
	}

	conds := h.routeConditions(t, "web")
	accepted := conditionOf(t, conds, string(gatewayv1.RouteConditionAccepted))
	if accepted.Status != metav1.ConditionTrue {
		t.Errorf("Accepted = %+v, want True", accepted)
	}
	if accepted.ObservedGeneration == 0 {
		t.Error("observedGeneration is zero; ArgoCD would report the route as progressing")
	}
}

// A failed apply must be visible on the resource, not only in the log.
func TestReconcileReportsAPublishFailureOnTheRoute(t *testing.T) {
	h := newHarness(t)
	h.setupClassAndGateway(t, "pangolin")
	h.create(t, h.newRoute("web", "web.example.com"))

	h.fake.FailApply(http.StatusInternalServerError)
	if err := h.reconcile(t); err == nil {
		t.Fatal("reconcile succeeded despite a failing apply")
	}

	accepted := conditionOf(t, h.routeConditions(t, "web"),
		string(gatewayv1.RouteConditionAccepted))
	if accepted.Status != metav1.ConditionFalse {
		t.Errorf("Accepted = %+v, want False", accepted)
	}
	if accepted.Reason != gateway.ReasonPublishFailed {
		t.Errorf("reason = %q, want %q", accepted.Reason, gateway.ReasonPublishFailed)
	}
}

func TestReconcilePrunesAResourceWhoseRouteIsGone(t *testing.T) {
	h := newHarness(t)
	h.setupClassAndGateway(t, "pangolin")
	route := h.newRoute("web", "web.example.com")
	h.create(t, route)

	h.mustReconcile(t)
	if _, ok := h.fake.Resources()["gw-demo-web"]; !ok {
		t.Fatal("the resource was never created")
	}

	if err := h.client.Delete(h.ctx, route); err != nil {
		t.Fatalf("deleting route: %v", err)
	}
	h.mustReconcile(t)

	if _, ok := h.fake.Resources()["gw-demo-web"]; ok {
		t.Error("the resource survived after its route was deleted")
	}
}

// Deleting the last route is an ordinary intent, not a defect: its resource must
// still be pruned rather than stranded.
func TestReconcilePrunesTheLastRemainingRoute(t *testing.T) {
	h := newHarness(t)
	h.setupClassAndGateway(t, "pangolin")
	route := h.newRoute("only", "only.example.com")
	h.create(t, route)

	h.mustReconcile(t)
	if err := h.client.Delete(h.ctx, route); err != nil {
		t.Fatalf("deleting route: %v", err)
	}
	h.mustReconcile(t)

	if got := h.fake.Resources(); len(got) != 0 {
		t.Errorf("resources = %v, want the last one pruned too", got)
	}
}

// The prune must never touch what a hand-written blueprint maintains.
func TestReconcileLeavesForeignResourcesAlone(t *testing.T) {
	h := newHarness(t)
	h.setupClassAndGateway(t, "pangolin")
	h.fake.Seed("manual-service", pangolin.PublicResource{
		Name: "hand written", Mode: pangolin.ModeHTTP, FullDomain: "manual.example.com",
	})

	h.mustReconcile(t)

	if _, ok := h.fake.Resources()["manual-service"]; !ok {
		t.Error("the prune deleted a resource without the gw- prefix")
	}
}

// A blocked DELETE (pangolin#3515's CrowdSec case) must not take the apply down.
func TestReconcileRetriesAFailedDeleteWithoutLosingTheApply(t *testing.T) {
	h := newHarness(t)
	h.setupClassAndGateway(t, "pangolin")
	h.fake.Seed("gw-demo-orphan", pangolin.PublicResource{
		Name: "demo/orphan", Mode: pangolin.ModeHTTP, FullDomain: "orphan.example.com",
	})
	h.create(t, h.newRoute("web", "web.example.com"))

	h.fake.FailDelete(http.StatusForbidden)
	if err := h.reconcile(t); err == nil {
		t.Fatal("reconcile hid a failing delete")
	}

	// The apply still went through, so the live route is published.
	if _, ok := h.fake.Resources()["gw-demo-web"]; !ok {
		t.Error("a blocked delete prevented the blueprint apply")
	}
	if _, ok := h.fake.Resources()["gw-demo-orphan"]; !ok {
		t.Error("the orphan vanished despite the delete being refused")
	}

	h.fake.FailDelete(0)
	h.mustReconcile(t)

	if _, ok := h.fake.Resources()["gw-demo-orphan"]; ok {
		t.Error("the orphan was not retried on the next pass")
	}
}

// A failed apply must not let the prune delete resources whose replacements were
// never created.
func TestReconcileSkipsThePruneWhenTheApplyFailed(t *testing.T) {
	h := newHarness(t)
	h.setupClassAndGateway(t, "pangolin")
	h.fake.Seed("gw-demo-orphan", pangolin.PublicResource{
		Name: "demo/orphan", Mode: pangolin.ModeHTTP, FullDomain: "orphan.example.com",
	})

	h.fake.FailApply(http.StatusInternalServerError)
	if err := h.reconcile(t); err == nil {
		t.Fatal("reconcile succeeded despite a failing apply")
	}

	if got := h.fake.Deleted(); len(got) != 0 {
		t.Errorf("the prune deleted %v after a failed apply", got)
	}
}

// A broken listing looks like "everything is gone"; deleting on that view would
// remove every resource this controller owns.
func TestReconcileSkipsThePruneWhenTheListingFails(t *testing.T) {
	h := newHarness(t)
	h.setupClassAndGateway(t, "pangolin")
	h.fake.Seed("gw-demo-orphan", pangolin.PublicResource{
		Name: "demo/orphan", Mode: pangolin.ModeHTTP, FullDomain: "orphan.example.com",
	})

	h.fake.FailList(http.StatusUnauthorized)
	if err := h.reconcile(t); err == nil {
		t.Fatal("reconcile succeeded despite a failing listing")
	}

	if got := h.fake.Deleted(); len(got) != 0 {
		t.Errorf("the prune deleted %v on a failed listing", got)
	}
}

func TestReconcileIgnoresRoutesOnAForeignClass(t *testing.T) {
	h := newHarness(t)
	h.create(t, &gatewayv1.GatewayClass{
		ObjectMeta: metav1.ObjectMeta{Name: "other"},
		Spec:       gatewayv1.GatewayClassSpec{ControllerName: "example.com/other"},
	})
	h.create(t, &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Namespace: h.namespace, Name: "gw"},
		Spec: gatewayv1.GatewaySpec{
			GatewayClassName: "other",
			Listeners: []gatewayv1.Listener{{
				Name: "http", Port: 80, Protocol: gatewayv1.HTTPProtocolType,
			}},
		},
	})
	h.create(t, h.newRoute("web", "web.example.com"))

	h.mustReconcile(t)

	bp, _ := h.fake.LastBlueprint()
	if len(bp.PublicResources) != 0 {
		t.Errorf("published %v for a foreign class", bp.PublicResources)
	}

	var r gatewayv1.HTTPRoute
	key := types.NamespacedName{Namespace: h.namespace, Name: "web"}
	if err := h.client.Get(h.ctx, key, &r); err != nil {
		t.Fatalf("getting route: %v", err)
	}
	for _, ps := range r.Status.Parents {
		if string(ps.ControllerName) == controllerName {
			t.Error("status was written on a route this controller does not serve")
		}
	}
}

// Repeated passes over unchanged input must stop writing, or every status write
// becomes a watch event that triggers the next pass.
func TestReconcileStopsWritingStatusOnceSettled(t *testing.T) {
	h := newHarness(t)
	h.setupClassAndGateway(t, "pangolin")
	h.create(t, h.newRoute("web", "web.example.com"))

	h.mustReconcile(t)

	var before gatewayv1.HTTPRoute
	key := types.NamespacedName{Namespace: h.namespace, Name: "web"}
	if err := h.client.Get(h.ctx, key, &before); err != nil {
		t.Fatalf("getting route: %v", err)
	}

	for i := range 3 {
		h.mustReconcile(t)

		var after gatewayv1.HTTPRoute
		if err := h.client.Get(h.ctx, key, &after); err != nil {
			t.Fatalf("getting route: %v", err)
		}
		if after.ResourceVersion != before.ResourceVersion {
			t.Fatalf("pass %d rewrote unchanged status (resourceVersion %s then %s)",
				i+1, before.ResourceVersion, after.ResourceVersion)
		}
	}
}

func TestReconcileSetsGatewayAndClassStatus(t *testing.T) {
	h := newHarness(t)
	h.setupClassAndGateway(t, "pangolin")

	h.mustReconcile(t)

	var gc gatewayv1.GatewayClass
	if err := h.client.Get(h.ctx, types.NamespacedName{Name: "pangolin"}, &gc); err != nil {
		t.Fatalf("getting GatewayClass: %v", err)
	}
	accepted := conditionOf(t, gc.Status.Conditions,
		string(gatewayv1.GatewayClassConditionStatusAccepted))
	if accepted.Status != metav1.ConditionTrue {
		t.Errorf("GatewayClass Accepted = %+v, want True", accepted)
	}

	var g gatewayv1.Gateway
	key := types.NamespacedName{Namespace: h.namespace, Name: "gw"}
	if err := h.client.Get(h.ctx, key, &g); err != nil {
		t.Fatalf("getting Gateway: %v", err)
	}
	for _, typ := range []string{
		string(gatewayv1.GatewayConditionAccepted),
		string(gatewayv1.GatewayConditionProgrammed),
	} {
		c := conditionOf(t, g.Status.Conditions, typ)
		if c.Status != metav1.ConditionTrue {
			t.Errorf("Gateway %s = %+v, want True", typ, c)
		}
	}
}

// Two routes claiming one hostname would fail the single apply and unpublish
// everything; the renderer must keep the blueprint applicable instead.
func TestReconcileKeepsPublishingWhenTwoRoutesClaimOneHostname(t *testing.T) {
	h := newHarness(t)
	h.setupClassAndGateway(t, "pangolin")
	h.create(t, h.newRoute("first", "shared.example.com"))
	h.create(t, h.newRoute("second", "shared.example.com"))
	h.create(t, h.newRoute("third", "third.example.com"))

	h.mustReconcile(t)

	live := h.fake.Resources()
	if _, ok := live["gw-demo-third"]; !ok {
		t.Error("an unrelated route was lost to someone else's hostname collision")
	}
	published := 0
	for _, name := range []string{"gw-demo-first", "gw-demo-second"} {
		if _, ok := live[name]; ok {
			published++
		}
	}
	if published != 1 {
		t.Errorf("%d of the two claimants were published, want exactly 1", published)
	}

	var rejected int
	for _, name := range []string{"first", "second"} {
		c := conditionOf(t, h.routeConditions(t, name), string(gatewayv1.RouteConditionAccepted))
		if c.Status == metav1.ConditionFalse && c.Reason == gateway.ReasonDuplicateHostname {
			rejected++
		}
	}
	if rejected != 1 {
		t.Errorf("%d routes report DuplicateHostname, want exactly 1", rejected)
	}
}

func TestReconcileRequeuesForPeriodicResync(t *testing.T) {
	h := newHarness(t)
	h.setupClassAndGateway(t, "pangolin")

	res, err := h.publisher.Reconcile(ctrl.LoggerInto(h.ctx, ctrl.Log), ctrl.Request{})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	// Pangolin is not watched, so drift there is repaired only by a timed pass.
	if res.RequeueAfter <= 0 {
		t.Errorf("RequeueAfter = %v, want a periodic resync", res.RequeueAfter)
	}
}

func TestReconcileWithNoRoutesAppliesAnEmptyBlueprint(t *testing.T) {
	h := newHarness(t)
	h.setupClassAndGateway(t, "pangolin")

	h.mustReconcile(t)

	bp, ok := h.fake.LastBlueprint()
	if !ok {
		t.Fatal("no blueprint reached the fake")
	}
	if len(bp.PublicResources) != 0 {
		t.Errorf("blueprint = %v, want empty", bp.PublicResources)
	}
}

// One mistyped site annotation must not take the whole apply down with it.
func TestReconcileRejectsOnlyTheRouteWithAnUnknownSite(t *testing.T) {
	h := newHarness(t)
	h.setupClassAndGateway(t, "pangolin")

	good := h.newRoute("good", "good.example.com")
	typo := h.newRoute("typo", "typo.example.com")
	typo.Annotations = map[string]string{gateway.AnnotationSite: "no-such-site"}
	h.create(t, good)
	h.create(t, typo)

	h.mustReconcile(t)

	live := h.fake.Resources()
	if _, ok := live["gw-demo-good"]; !ok {
		t.Error("a valid route was lost to its neighbour's site typo")
	}
	if _, ok := live["gw-demo-typo"]; ok {
		t.Error("a route naming an unknown site was published")
	}

	c := conditionOf(t, h.routeConditions(t, "typo"), string(gatewayv1.RouteConditionAccepted))
	if c.Status != metav1.ConditionFalse || c.Reason != gateway.ReasonUnknownSite {
		t.Errorf("Accepted = %+v, want False/%s", c, gateway.ReasonUnknownSite)
	}
}

// A key without the listSites action must degrade to the previous behaviour
// rather than reject every route.
func TestReconcileKeepsPublishingWhenSitesCannotBeListed(t *testing.T) {
	h := newHarness(t)
	h.setupClassAndGateway(t, "pangolin")
	h.create(t, h.newRoute("web", "web.example.com"))

	h.fake.FailSites(http.StatusForbidden)
	h.mustReconcile(t)

	if _, ok := h.fake.Resources()["gw-demo-web"]; !ok {
		t.Error("routes were withheld although only the site listing was unavailable")
	}
}

// A rehearsal that reports a plain Accepted=True reads as a successful rollout,
// which defeats the point of running against a real instance with --dry-run.
func TestReconcileMarksStatusAsDryRun(t *testing.T) {
	h := newHarness(t)
	h.publisher.DryRun = true
	h.setupClassAndGateway(t, "pangolin")
	h.create(t, h.newRoute("web", "web.example.com"))

	h.mustReconcile(t)

	c := conditionOf(t, h.routeConditions(t, "web"), string(gatewayv1.RouteConditionAccepted))
	if c.Status != metav1.ConditionTrue {
		t.Errorf("Accepted = %+v, want True: the route is accepted, just unpublished", c)
	}
	if c.Reason != gateway.ReasonDryRun {
		t.Errorf("reason = %q, want %q", c.Reason, gateway.ReasonDryRun)
	}
}

// The healthcheck has to survive the whole path: annotation, render, JSON, wire.
func TestReconcilePublishesAHealthcheck(t *testing.T) {
	h := newHarness(t)
	h.setupClassAndGateway(t, "pangolin")

	r := h.newRoute("web", "web.example.com")
	r.Annotations = map[string]string{
		gateway.AnnotationHealthcheckPath:     "/healthz",
		gateway.AnnotationHealthcheckInterval: "15",
	}
	h.create(t, r)

	h.mustReconcile(t)

	entry, ok := h.fake.Resources()["gw-demo-web"]
	if !ok {
		t.Fatalf("route not published; fake holds %v", h.fake.Resources())
	}
	hc := entry.Targets[0].Healthcheck
	if hc == nil {
		t.Fatal("the healthcheck did not reach the fake")
	}
	if hc.Path != "/healthz" || hc.Interval != 15 || hc.Timeout != gateway.DefaultHealthcheckTimeout {
		t.Errorf("healthcheck = %+v, want path /healthz, interval 15, default timeout", hc)
	}
	if hc.Hostname != entry.Targets[0].Hostname || hc.Port != entry.Targets[0].Port {
		t.Errorf("healthcheck addresses %s:%d, want the target's %s:%d",
			hc.Hostname, hc.Port, entry.Targets[0].Hostname, entry.Targets[0].Port)
	}
}
