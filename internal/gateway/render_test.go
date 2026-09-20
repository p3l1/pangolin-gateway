// SPDX-License-Identifier: AGPL-3.0-only

package gateway

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/p3l1/pangolin-gateway/internal/pangolin"
)

const testControllerName = "p3l1.de/pangolin"

func ptr[T any](v T) *T { return &v }

// ourClass is a GatewayClass this controller serves; foreignClass is one it must ignore.
func ourClass(name string) gatewayv1.GatewayClass {
	return gatewayv1.GatewayClass{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       gatewayv1.GatewayClassSpec{ControllerName: testControllerName},
	}
}

func foreignClass(name string) gatewayv1.GatewayClass {
	return gatewayv1.GatewayClass{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       gatewayv1.GatewayClassSpec{ControllerName: "example.com/other"},
	}
}

func gateway(namespace, name, className string) gatewayv1.Gateway {
	return gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Spec:       gatewayv1.GatewaySpec{GatewayClassName: gatewayv1.ObjectName(className)},
	}
}

type routeOpt func(*gatewayv1.HTTPRoute)

func route(namespace, name string, opts ...routeOpt) gatewayv1.HTTPRoute {
	r := gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:  namespace,
			Name:       name,
			Generation: 1,
		},
		Spec: gatewayv1.HTTPRouteSpec{
			CommonRouteSpec: gatewayv1.CommonRouteSpec{
				ParentRefs: []gatewayv1.ParentReference{{Name: "gw"}},
			},
			Hostnames: []gatewayv1.Hostname{gatewayv1.Hostname(name + ".example.com")},
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
	for _, o := range opts {
		o(&r)
	}
	return r
}

func withHostnames(hs ...string) routeOpt {
	return func(r *gatewayv1.HTTPRoute) {
		r.Spec.Hostnames = nil
		for _, h := range hs {
			r.Spec.Hostnames = append(r.Spec.Hostnames, gatewayv1.Hostname(h))
		}
	}
}

func withAnnotations(kv map[string]string) routeOpt {
	return func(r *gatewayv1.HTTPRoute) { r.Annotations = kv }
}

func withParentRefs(refs ...gatewayv1.ParentReference) routeOpt {
	return func(r *gatewayv1.HTTPRoute) { r.Spec.ParentRefs = refs }
}

func withRules(rules ...gatewayv1.HTTPRouteRule) routeOpt {
	return func(r *gatewayv1.HTTPRoute) { r.Spec.Rules = rules }
}

func withCreation(t time.Time) routeOpt {
	return func(r *gatewayv1.HTTPRoute) { r.CreationTimestamp = metav1.NewTime(t) }
}

func backendRule(name string, port int32) gatewayv1.HTTPRouteRule {
	return gatewayv1.HTTPRouteRule{
		BackendRefs: []gatewayv1.HTTPBackendRef{{
			BackendRef: gatewayv1.BackendRef{
				BackendObjectReference: gatewayv1.BackendObjectReference{
					Name: gatewayv1.ObjectName(name),
					Port: ptr(gatewayv1.PortNumber(port)),
				},
			},
		}},
	}
}

func noPortRule() gatewayv1.HTTPRouteRule {
	rule := backendRule("web", 8080)
	rule.BackendRefs[0].Port = nil
	return rule
}

func defaultInputs(routes ...gatewayv1.HTTPRoute) Inputs {
	return Inputs{
		Routes:         routes,
		Gateways:       []gatewayv1.Gateway{gateway("demo", "gw", "pangolin")},
		GatewayClasses: []gatewayv1.GatewayClass{ourClass("pangolin")},
		ControllerName: testControllerName,
		DefaultSite:    "default-site",
	}
}

func verdictFor(t *testing.T, verdicts map[types.NamespacedName]Verdict, ns, name string) Verdict {
	t.Helper()

	v, ok := verdicts[types.NamespacedName{Namespace: ns, Name: name}]
	if !ok {
		t.Fatalf("no verdict for %s/%s; have %v", ns, name, verdicts)
	}
	return v
}

func TestRenderMapsARouteToAPublicResource(t *testing.T) {
	bp, verdicts := Render(defaultInputs(route("demo", "web")))

	entry, ok := bp.PublicResources["gw-demo-web"]
	if !ok {
		t.Fatalf("no gw-demo-web entry; have %v", bp.PublicResources)
	}
	if got, want := entry.Mode, pangolin.ModeHTTP; got != want {
		t.Errorf("mode = %q, want %q", got, want)
	}
	if got, want := entry.FullDomain, "web.example.com"; got != want {
		t.Errorf("full-domain = %q, want %q", got, want)
	}
	if entry.Auth == nil || !entry.Auth.SSOEnabled {
		t.Errorf("auth = %+v, want sso-enabled true by default", entry.Auth)
	}
	if len(entry.Targets) != 1 {
		t.Fatalf("targets = %+v, want exactly one", entry.Targets)
	}
	target := entry.Targets[0]
	if got, want := target.Hostname, "web.demo.svc.cluster.local"; got != want {
		t.Errorf("target hostname = %q, want %q", got, want)
	}
	if got, want := target.Port, int32(8080); got != want {
		t.Errorf("target port = %d, want %d", got, want)
	}
	if got, want := target.Site, "default-site"; got != want {
		t.Errorf("target site = %q, want %q", got, want)
	}

	v := verdictFor(t, verdicts, "demo", "web")
	if v.Accepted.Status != metav1.ConditionTrue {
		t.Errorf("Accepted = %+v, want True", v.Accepted)
	}
	if v.ResolvedRefs.Status != metav1.ConditionTrue {
		t.Errorf("ResolvedRefs = %+v, want True", v.ResolvedRefs)
	}
	if v.Key != "gw-demo-web" {
		t.Errorf("key = %q, want gw-demo-web", v.Key)
	}
}

func TestRenderHonoursAnnotations(t *testing.T) {
	r := route("demo", "web", withAnnotations(map[string]string{
		AnnotationSSO:  "false",
		AnnotationSite: "edge-site",
	}))

	bp, _ := Render(defaultInputs(r))

	entry := bp.PublicResources["gw-demo-web"]
	if entry.Auth == nil || entry.Auth.SSOEnabled {
		t.Errorf("auth = %+v, want sso-enabled false", entry.Auth)
	}
	if got, want := entry.Targets[0].Site, "edge-site"; got != want {
		t.Errorf("site = %q, want %q", got, want)
	}
}

// Fail closed: an unreadable sso value must not silently publish a service
// unprotected, and must not silently protect one the author meant to open.
func TestRenderRejectsAMalformedSSOAnnotation(t *testing.T) {
	r := route("demo", "web", withAnnotations(map[string]string{AnnotationSSO: "yes"}))

	bp, verdicts := Render(defaultInputs(r))

	if _, published := bp.PublicResources["gw-demo-web"]; published {
		t.Error("route with a malformed sso annotation was published")
	}
	v := verdictFor(t, verdicts, "demo", "web")
	if v.Accepted.Status != metav1.ConditionFalse {
		t.Errorf("Accepted = %+v, want False", v.Accepted)
	}
	if v.Accepted.Reason != string(gatewayv1.RouteReasonUnsupportedValue) {
		t.Errorf("reason = %q, want UnsupportedValue", v.Accepted.Reason)
	}
}

func TestRenderIgnoresRoutesOnAForeignClass(t *testing.T) {
	in := defaultInputs(route("demo", "web"))
	in.Gateways = []gatewayv1.Gateway{gateway("demo", "gw", "other")}
	in.GatewayClasses = []gatewayv1.GatewayClass{foreignClass("other")}

	bp, verdicts := Render(in)

	if len(bp.PublicResources) != 0 {
		t.Errorf("published %v, want nothing for a foreign class", bp.PublicResources)
	}
	// No verdict at all: writing status on a route we do not serve would clobber
	// the owning controller's parent entry.
	if _, ok := verdicts[types.NamespacedName{Namespace: "demo", Name: "web"}]; ok {
		t.Error("a verdict was produced for a route this controller does not serve")
	}
}

// A route may name several parents; only the ones this controller serves get a
// status entry, and one served parent is enough to publish the route.
func TestRenderHandlesMixedParentRefs(t *testing.T) {
	r := route("demo", "web", withParentRefs(
		gatewayv1.ParentReference{Name: "gw"},
		gatewayv1.ParentReference{Name: "other-gw"},
	))
	in := defaultInputs(r)
	in.Gateways = append(in.Gateways, gateway("demo", "other-gw", "other"))
	in.GatewayClasses = append(in.GatewayClasses, foreignClass("other"))

	bp, verdicts := Render(in)

	if _, ok := bp.PublicResources["gw-demo-web"]; !ok {
		t.Error("route with one served parent was not published")
	}
	v := verdictFor(t, verdicts, "demo", "web")
	if len(v.ParentRefs) != 1 {
		t.Fatalf("got %d served parent refs, want 1: %+v", len(v.ParentRefs), v.ParentRefs)
	}
	if got, want := string(v.ParentRefs[0].Name), "gw"; got != want {
		t.Errorf("served parent = %q, want %q", got, want)
	}
}

func TestRenderReportsAMissingParent(t *testing.T) {
	r := route("demo", "web", withParentRefs(gatewayv1.ParentReference{Name: "absent"}))

	_, verdicts := Render(defaultInputs(r))

	// A parentRef naming no existing Gateway is unresolvable rather than foreign,
	// so it is reported rather than silently ignored.
	v := verdictFor(t, verdicts, "demo", "web")
	if v.Accepted.Status != metav1.ConditionFalse {
		t.Errorf("Accepted = %+v, want False", v.Accepted)
	}
	if got, want := v.Accepted.Reason, string(gatewayv1.RouteReasonNoMatchingParent); got != want {
		t.Errorf("reason = %q, want %q", got, want)
	}
}

func TestRenderRejectsUnsupportedHostnames(t *testing.T) {
	cases := map[string]struct {
		opt  routeOpt
		want string
	}{
		"none":      {withHostnames(), string(gatewayv1.RouteReasonUnsupportedValue)},
		"several":   {withHostnames("a.example.com", "b.example.com"), string(gatewayv1.RouteReasonUnsupportedValue)},
		"wildcard":  {withHostnames("*.example.com"), string(gatewayv1.RouteReasonUnsupportedValue)},
		"supported": {withHostnames("a.example.com"), ""},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			bp, verdicts := Render(defaultInputs(route("demo", "web", tc.opt)))
			v := verdictFor(t, verdicts, "demo", "web")

			if tc.want == "" {
				if v.Accepted.Status != metav1.ConditionTrue {
					t.Fatalf("Accepted = %+v, want True", v.Accepted)
				}
				return
			}
			if v.Accepted.Status != metav1.ConditionFalse {
				t.Fatalf("Accepted = %+v, want False", v.Accepted)
			}
			if v.Accepted.Reason != tc.want {
				t.Errorf("reason = %q, want %q", v.Accepted.Reason, tc.want)
			}
			if _, published := bp.PublicResources["gw-demo-web"]; published {
				t.Error("rejected route was published anyway")
			}
		})
	}
}

// Publishing a route while dropping its filters would proxy the whole domain to
// one backend — traffic behaviour contradicting what the route says.
func TestRenderRejectsRulesItCannotRepresent(t *testing.T) {
	cases := map[string]struct {
		rules  []gatewayv1.HTTPRouteRule
		reason string
	}{
		"two rules": {
			rules:  []gatewayv1.HTTPRouteRule{backendRule("web", 8080), backendRule("api", 9090)},
			reason: string(gatewayv1.RouteReasonUnsupportedValue),
		},
		"a filter": {
			rules: []gatewayv1.HTTPRouteRule{{
				BackendRefs: backendRule("web", 8080).BackendRefs,
				Filters: []gatewayv1.HTTPRouteFilter{{
					Type: gatewayv1.HTTPRouteFilterRequestRedirect,
				}},
			}},
			reason: string(gatewayv1.RouteReasonUnsupportedValue),
		},
		"two backendRefs": {
			rules: []gatewayv1.HTTPRouteRule{{
				BackendRefs: append(
					backendRule("web", 8080).BackendRefs,
					backendRule("api", 9090).BackendRefs...),
			}},
			reason: string(gatewayv1.RouteReasonUnsupportedValue),
		},
		"no rules": {
			rules:  nil,
			reason: string(gatewayv1.RouteReasonUnsupportedValue),
		},
		// A backendRef without a port names no endpoint to reach. The ref itself
		// resolves fine; the value is what this implementation cannot use.
		"no port": {
			rules:  []gatewayv1.HTTPRouteRule{noPortRule()},
			reason: string(gatewayv1.RouteReasonUnsupportedValue),
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			bp, verdicts := Render(defaultInputs(route("demo", "web", withRules(tc.rules...))))

			if _, published := bp.PublicResources["gw-demo-web"]; published {
				t.Error("route was published despite an unsupported rule set")
			}
			v := verdictFor(t, verdicts, "demo", "web")
			if v.Accepted.Status != metav1.ConditionFalse {
				t.Fatalf("Accepted = %+v, want False", v.Accepted)
			}
			if v.Accepted.Reason != tc.reason {
				t.Errorf("reason = %q, want %q", v.Accepted.Reason, tc.reason)
			}
		})
	}
}

func TestRenderRejectsBackendsItCannotResolve(t *testing.T) {
	nonService := backendRule("web", 8080)
	nonService.BackendRefs[0].Kind = ptr(gatewayv1.Kind("S3Bucket"))

	crossNamespace := backendRule("web", 8080)
	crossNamespace.BackendRefs[0].Namespace = ptr(gatewayv1.Namespace("other"))

	cases := map[string]struct {
		rule   gatewayv1.HTTPRouteRule
		reason string
	}{
		"not a service":   {nonService, string(gatewayv1.RouteReasonInvalidKind)},
		"cross namespace": {crossNamespace, string(gatewayv1.RouteReasonRefNotPermitted)},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			bp, verdicts := Render(defaultInputs(route("demo", "web", withRules(tc.rule))))

			if _, published := bp.PublicResources["gw-demo-web"]; published {
				t.Error("route was published despite an unresolvable backend")
			}
			v := verdictFor(t, verdicts, "demo", "web")
			if v.ResolvedRefs.Status != metav1.ConditionFalse {
				t.Fatalf("ResolvedRefs = %+v, want False", v.ResolvedRefs)
			}
			if v.ResolvedRefs.Reason != tc.reason {
				t.Errorf("reason = %q, want %q", v.ResolvedRefs.Reason, tc.reason)
			}
		})
	}
}

// Namespace "a-b" with name "c" and namespace "a" with name "b-c" both render
// "gw-a-b-c". Without detection the two routes fight over one Pangolin resource.
func TestRenderDetectsKeyCollisions(t *testing.T) {
	older := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	newer := older.Add(time.Hour)

	in := defaultInputs(
		route("a-b", "c", withHostnames("first.example.com"), withCreation(older)),
		route("a", "b-c", withHostnames("second.example.com"), withCreation(newer)),
	)
	in.Gateways = []gatewayv1.Gateway{gateway("a-b", "gw", "pangolin"), gateway("a", "gw", "pangolin")}

	bp, verdicts := Render(in)

	if len(bp.PublicResources) != 1 {
		t.Fatalf("published %d resources, want 1: %v", len(bp.PublicResources), bp.PublicResources)
	}
	if got, want := bp.PublicResources["gw-a-b-c"].FullDomain, "first.example.com"; got != want {
		t.Errorf("published full-domain = %q, want the older route's %q", got, want)
	}

	if v := verdictFor(t, verdicts, "a-b", "c"); v.Accepted.Status != metav1.ConditionTrue {
		t.Errorf("older route Accepted = %+v, want True", v.Accepted)
	}
	loser := verdictFor(t, verdicts, "a", "b-c")
	if loser.Accepted.Status != metav1.ConditionFalse {
		t.Errorf("younger route Accepted = %+v, want False", loser.Accepted)
	}
	if loser.Accepted.Reason != ReasonDuplicateKey {
		t.Errorf("reason = %q, want %q", loser.Accepted.Reason, ReasonDuplicateKey)
	}
}

// full-domain must be unique across the blueprint, so a duplicate would fail the
// single apply call and unpublish every other route with it.
func TestRenderDetectsHostnameCollisions(t *testing.T) {
	older := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	newer := older.Add(time.Hour)

	in := defaultInputs(
		route("demo", "first", withHostnames("shared.example.com"), withCreation(older)),
		route("demo", "second", withHostnames("shared.example.com"), withCreation(newer)),
		route("demo", "third", withHostnames("fine.example.com"), withCreation(newer)),
	)

	bp, verdicts := Render(in)

	if _, ok := bp.PublicResources["gw-demo-first"]; !ok {
		t.Error("the older claimant of the hostname was not published")
	}
	if _, ok := bp.PublicResources["gw-demo-second"]; ok {
		t.Error("the younger claimant of the hostname was published")
	}
	if _, ok := bp.PublicResources["gw-demo-third"]; !ok {
		t.Error("an unrelated route was dropped by someone else's collision")
	}

	loser := verdictFor(t, verdicts, "demo", "second")
	if loser.Accepted.Status != metav1.ConditionFalse {
		t.Errorf("younger route Accepted = %+v, want False", loser.Accepted)
	}
	if loser.Accepted.Reason != ReasonDuplicateHostname {
		t.Errorf("reason = %q, want %q", loser.Accepted.Reason, ReasonDuplicateHostname)
	}
}

// Ties on creationTimestamp must not make the published set depend on map order:
// a flapping blueprint would rewrite Pangolin on every pass.
func TestRenderIsDeterministicOnEqualTimestamps(t *testing.T) {
	same := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	in := defaultInputs(
		route("demo", "zebra", withHostnames("shared.example.com"), withCreation(same)),
		route("demo", "alpha", withHostnames("shared.example.com"), withCreation(same)),
	)

	first, _ := Render(in)
	for range 20 {
		again, _ := Render(in)
		if len(again.PublicResources) != len(first.PublicResources) {
			t.Fatalf("resource count changed between renders: %d then %d",
				len(first.PublicResources), len(again.PublicResources))
		}
		for key := range first.PublicResources {
			if _, ok := again.PublicResources[key]; !ok {
				t.Fatalf("key %q present in one render and absent in another", key)
			}
		}
	}
}

func TestRenderProducesAnEmptyBlueprintForNoRoutes(t *testing.T) {
	bp, verdicts := Render(defaultInputs())

	if bp.PublicResources == nil {
		t.Error("PublicResources is nil; an empty map is the explicit 'nothing owned' statement")
	}
	if len(verdicts) != 0 {
		t.Errorf("verdicts = %v, want none", verdicts)
	}
}

// Pangolin throws inside its apply transaction for one unknown site, so a single
// mistyped annotation would otherwise unpublish every route in the cluster.
func TestRenderRejectsAnUnknownSite(t *testing.T) {
	in := defaultInputs(
		route("demo", "good", withAnnotations(map[string]string{AnnotationSite: "real-site"})),
		route("demo", "typo", withAnnotations(map[string]string{AnnotationSite: "raelsite"})),
	)
	in.KnownSites = map[string]bool{"real-site": true, "default-site": true}

	bp, verdicts := Render(in)

	if _, ok := bp.PublicResources["gw-demo-good"]; !ok {
		t.Error("a route naming a real site was dropped by its neighbour's typo")
	}
	if _, ok := bp.PublicResources["gw-demo-typo"]; ok {
		t.Error("a route naming an unknown site was published")
	}
	v := verdictFor(t, verdicts, "demo", "typo")
	if v.Accepted.Status != metav1.ConditionFalse || v.Accepted.Reason != ReasonUnknownSite {
		t.Errorf("Accepted = %+v, want False/%s", v.Accepted, ReasonUnknownSite)
	}
}

// The default site is subject to the same check; a typo in the flag must not
// take the whole apply down either.
func TestRenderRejectsAnUnknownDefaultSite(t *testing.T) {
	in := defaultInputs(route("demo", "web"))
	in.DefaultSite = "missing-site"
	in.KnownSites = map[string]bool{"real-site": true}

	bp, verdicts := Render(in)

	if len(bp.PublicResources) != 0 {
		t.Errorf("published %v despite an unknown default site", bp.PublicResources)
	}
	if v := verdictFor(t, verdicts, "demo", "web"); v.Accepted.Reason != ReasonUnknownSite {
		t.Errorf("reason = %q, want %q", v.Accepted.Reason, ReasonUnknownSite)
	}
}

// A nil map means the listing was unavailable — typically a key without the
// listSites action. The controller must keep working, not reject everything.
func TestRenderSkipsTheSiteCheckWhenSitesAreUnknown(t *testing.T) {
	in := defaultInputs(route("demo", "web",
		withAnnotations(map[string]string{AnnotationSite: "whatever"})))
	in.KnownSites = nil

	bp, _ := Render(in)

	if _, ok := bp.PublicResources["gw-demo-web"]; !ok {
		t.Error("routes were rejected although the site list was unavailable")
	}
}

// Pangolin shows this name in its dashboard, so a migrated resource should be
// able to keep the one people already recognise.
func TestRenderHonoursADisplayName(t *testing.T) {
	r := route("demo", "web", withAnnotations(map[string]string{
		AnnotationName: "Regenalarm",
	}))

	bp, _ := Render(defaultInputs(r))

	if got, want := bp.PublicResources["gw-demo-web"].Name, "Regenalarm"; got != want {
		t.Errorf("name = %q, want %q", got, want)
	}
}

func TestRenderNamesAResourceAfterItsRouteByDefault(t *testing.T) {
	bp, _ := Render(defaultInputs(route("demo", "web")))

	if got, want := bp.PublicResources["gw-demo-web"].Name, "demo/web"; got != want {
		t.Errorf("name = %q, want %q", got, want)
	}
}

// An empty annotation is a mistake rather than a request for an empty name.
func TestRenderRejectsABlankDisplayName(t *testing.T) {
	r := route("demo", "web", withAnnotations(map[string]string{AnnotationName: "   "}))

	bp, verdicts := Render(defaultInputs(r))

	if _, published := bp.PublicResources["gw-demo-web"]; published {
		t.Error("route with a blank display name was published")
	}
	if v := verdictFor(t, verdicts, "demo", "web"); v.Accepted.Status != metav1.ConditionFalse {
		t.Errorf("Accepted = %+v, want False", v.Accepted)
	}
}
