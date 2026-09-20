// SPDX-License-Identifier: AGPL-3.0-only

package gateway

import (
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/p3l1/pangolin-gateway/internal/pangolin"
)

func private() routeOpt {
	return func(r *gatewayv1.HTTPRoute) {
		if r.Annotations == nil {
			r.Annotations = map[string]string{}
		}
		r.Annotations[AnnotationVisibility] = VisibilityPrivate
	}
}

func annotate(kv map[string]string) routeOpt {
	return func(r *gatewayv1.HTTPRoute) {
		if r.Annotations == nil {
			r.Annotations = map[string]string{}
		}
		for k, v := range kv {
			r.Annotations[k] = v
		}
	}
}

// rejectedRoute renders one route and returns why it was rejected, failing the
// test if it was published into either section instead.
func rejectedRoute(t *testing.T, opts ...routeOpt) string {
	t.Helper()

	bp, verdicts := Render(defaultInputs(route("demo", "web", opts...)))

	if _, ok := bp.PublicResources["gw-demo-web"]; ok {
		t.Fatal("route was published as a public resource, want it rejected")
	}
	if _, ok := bp.PrivateResources["gw-demo-web"]; ok {
		t.Fatal("route was published as a private resource, want it rejected")
	}
	v := verdictFor(t, verdicts, "demo", "web")
	if v.Accepted.Status != metav1.ConditionFalse {
		t.Errorf("Accepted = %+v, want False", v.Accepted)
	}
	if got, want := v.Accepted.Reason, string(gatewayv1.RouteReasonUnsupportedValue); got != want {
		t.Errorf("reason = %q, want %q", got, want)
	}
	return v.Accepted.Message
}

func TestRenderMapsAPrivateRouteToAPrivateResource(t *testing.T) {
	bp, verdicts := Render(defaultInputs(route("demo", "web", private())))

	if _, ok := bp.PublicResources["gw-demo-web"]; ok {
		t.Error("a private route reached public-resources")
	}
	entry, ok := bp.PrivateResources["gw-demo-web"]
	if !ok {
		t.Fatalf("no gw-demo-web in private-resources; have %v", bp.PrivateResources)
	}

	if got, want := entry.Mode, pangolin.ModeHTTP; got != want {
		t.Errorf("mode = %q, want %q", got, want)
	}
	if got, want := entry.Name, "demo/web"; got != want {
		t.Errorf("name = %q, want %q", got, want)
	}
	if got, want := entry.FullDomain, "web.example.com"; got != want {
		t.Errorf("full-domain = %q, want %q", got, want)
	}
	if got, want := entry.Destination, "web.demo.svc.cluster.local"; got != want {
		t.Errorf("destination = %q, want %q", got, want)
	}
	if got, want := entry.DestinationPort, int32(8080); got != want {
		t.Errorf("destination-port = %d, want %d", got, want)
	}
	if got, want := entry.Scheme, pangolin.SchemeHTTP; got != want {
		t.Errorf("scheme = %q, want %q", got, want)
	}
	// The array form; Pangolin deprecated the singular site key.
	if len(entry.Sites) != 1 || entry.Sites[0] != "default-site" {
		t.Errorf("sites = %v, want [default-site]", entry.Sites)
	}

	if v := verdictFor(t, verdicts, "demo", "web"); v.Accepted.Status != metav1.ConditionTrue {
		t.Errorf("Accepted = %+v, want True", v.Accepted)
	}
}

func TestRenderPublishesPubliclyByDefault(t *testing.T) {
	for name, opt := range map[string]routeOpt{
		"no annotation": func(*gatewayv1.HTTPRoute) {},
		"explicit":      annotate(map[string]string{AnnotationVisibility: VisibilityPublic}),
	} {
		t.Run(name, func(t *testing.T) {
			bp, _ := Render(defaultInputs(route("demo", "web", opt)))

			if _, ok := bp.PublicResources["gw-demo-web"]; !ok {
				t.Errorf("not published publicly; have %v", bp.PublicResources)
			}
			if len(bp.PrivateResources) != 0 {
				t.Errorf("private-resources = %v, want empty", bp.PrivateResources)
			}
		})
	}
}

func TestRenderRejectsAnUnknownVisibility(t *testing.T) {
	msg := rejectedRoute(t, annotate(map[string]string{AnnotationVisibility: "internal"}))

	if !strings.Contains(msg, "internal") {
		t.Errorf("message %q does not name the offending value", msg)
	}
}

func TestRenderHonoursSiteAndDisplayNameOnAPrivateRoute(t *testing.T) {
	in := defaultInputs(route("demo", "web", private(), annotate(map[string]string{
		AnnotationSite: "edge-site",
		AnnotationName: "Demo Web",
	})))
	in.KnownSites = map[string]bool{"edge-site": true}

	bp, verdicts := Render(in)

	entry, ok := bp.PrivateResources["gw-demo-web"]
	if !ok {
		t.Fatalf("rejected: %s", verdictFor(t, verdicts, "demo", "web").Accepted.Message)
	}
	if len(entry.Sites) != 1 || entry.Sites[0] != "edge-site" {
		t.Errorf("sites = %v, want [edge-site]", entry.Sites)
	}
	if got, want := entry.Name, "Demo Web"; got != want {
		t.Errorf("name = %q, want %q", got, want)
	}
}

// An unknown site throws inside the apply transaction for a private resource
// just as it does for a public one, so the whole document would be rejected.
func TestRenderRejectsAPrivateRouteWithAnUnknownSite(t *testing.T) {
	in := defaultInputs(route("demo", "web", private(),
		annotate(map[string]string{AnnotationSite: "nowhere"})))
	in.KnownSites = map[string]bool{"default-site": true}

	bp, verdicts := Render(in)

	if len(bp.PrivateResources) != 0 {
		t.Errorf("published %v, want the route rejected", bp.PrivateResources)
	}
	if v := verdictFor(t, verdicts, "demo", "web"); v.Accepted.Reason != ReasonUnknownSite {
		t.Errorf("reason = %q, want %q", v.Accepted.Reason, ReasonUnknownSite)
	}
}

func TestRenderCarriesRolesAndUsersOntoAPrivateResource(t *testing.T) {
	bp, verdicts := Render(defaultInputs(route("demo", "web", private(), annotate(
		map[string]string{
			AnnotationRoles: "Member, Operators",
			AnnotationUsers: "alice@example.com,bob@example.com",
		}))))

	entry, ok := bp.PrivateResources["gw-demo-web"]
	if !ok {
		t.Fatalf("rejected: %s", verdictFor(t, verdicts, "demo", "web").Accepted.Message)
	}
	if want := []string{"Member", "Operators"}; !equalStrings(entry.Roles, want) {
		t.Errorf("roles = %v, want %v", entry.Roles, want)
	}
	want := []string{"alice@example.com", "bob@example.com"}
	if !equalStrings(entry.Users, want) {
		t.Errorf("users = %v, want %v", entry.Users, want)
	}
}

// Without a grant only org admins reach the resource. That is a safe default,
// not a broken one, so it must publish rather than be rejected.
func TestRenderPublishesAPrivateRouteWithoutGrants(t *testing.T) {
	bp, verdicts := Render(defaultInputs(route("demo", "web", private())))

	entry, ok := bp.PrivateResources["gw-demo-web"]
	if !ok {
		t.Fatalf("rejected: %s", verdictFor(t, verdicts, "demo", "web").Accepted.Message)
	}
	if len(entry.Roles) != 0 || len(entry.Users) != 0 {
		t.Errorf("roles = %v, users = %v, want neither", entry.Roles, entry.Users)
	}
}

func TestRenderRejectsEmptyEntriesInRolesAndUsers(t *testing.T) {
	for name, kv := range map[string]map[string]string{
		"trailing comma": {AnnotationRoles: "Member,"},
		"empty middle":   {AnnotationUsers: "a@example.com,,b@example.com"},
		"only separator": {AnnotationRoles: ","},
		"blank":          {AnnotationUsers: "   "},
	} {
		t.Run(name, func(t *testing.T) {
			msg := rejectedRoute(t, private(), annotate(kv))
			if !strings.Contains(msg, "empty") {
				t.Errorf("message %q does not say the list has an empty entry", msg)
			}
		})
	}
}

// Pangolin's private resources have no auth block, no targets and no rules.
// Silently dropping any of these would publish something other than what the
// route says, so each is refused by name.
func TestRenderRejectsAnnotationsAPrivateResourceCannotHonour(t *testing.T) {
	for name, tc := range map[string]struct {
		kv   map[string]string
		want string
	}{
		"sso": {
			map[string]string{AnnotationSSO: "false"},
			AnnotationSSO,
		},
		"sso true is still meaningless": {
			map[string]string{AnnotationSSO: "true"},
			AnnotationSSO,
		},
		"healthcheck": {
			map[string]string{AnnotationHealthcheckPath: "/healthz"},
			AnnotationHealthcheckPath,
		},
		"healthcheck status": {
			map[string]string{AnnotationHealthcheckStatus: "400"},
			AnnotationHealthcheckStatus,
		},
		"healthcheck method": {
			map[string]string{AnnotationHealthcheckMethod: "HEAD"},
			AnnotationHealthcheckMethod,
		},
		"healthcheck follow-redirects": {
			map[string]string{AnnotationHealthcheckFollowRedirects: "false"},
			AnnotationHealthcheckFollowRedirects,
		},
		"healthcheck mode": {
			map[string]string{AnnotationHealthcheckMode: pangolin.HealthcheckModeTCP},
			AnnotationHealthcheckMode,
		},
		"basic auth": {
			map[string]string{AnnotationBasicAuth: "true"},
			AnnotationBasicAuth,
		},
		"access rules": {
			map[string]string{
				AnnotationAccessRules: "- action: allow\n  match: path\n  value: /x\n",
			},
			AnnotationAccessRules,
		},
	} {
		t.Run(name, func(t *testing.T) {
			msg := rejectedRoute(t, private(), annotate(tc.kv))
			if !strings.Contains(msg, tc.want) {
				t.Errorf("message %q does not name %q", msg, tc.want)
			}
		})
	}
}

// Roles and users have no meaning on a proxied resource, and quietly ignoring
// them would leave the author believing access is restricted.
func TestRenderRejectsGrantsOnAPublicRoute(t *testing.T) {
	for _, annotation := range []string{AnnotationRoles, AnnotationUsers} {
		t.Run(annotation, func(t *testing.T) {
			msg := rejectedRoute(t, annotate(map[string]string{annotation: "Member"}))
			if !strings.Contains(msg, annotation) {
				t.Errorf("message %q does not name %q", msg, annotation)
			}
			if !strings.Contains(msg, VisibilityPrivate) {
				t.Errorf("message %q does not say which visibility honours it", msg)
			}
		})
	}
}

// Pangolin checks full-domain uniqueness per section, and the check on the
// private side is narrower than it looks. Two routes claiming one hostname is a
// mistake either way, so the renderer settles it across both sections.
func TestRenderResolvesAHostnameCollisionAcrossSections(t *testing.T) {
	older := route("demo", "priv", private(),
		withHostnames("shared.example.com"),
		withCreation(time.Unix(1000, 0)))
	newer := route("demo", "pub",
		withHostnames("shared.example.com"),
		withCreation(time.Unix(2000, 0)))

	bp, verdicts := Render(defaultInputs(older, newer))

	if _, ok := bp.PrivateResources["gw-demo-priv"]; !ok {
		t.Errorf("the older route lost its hostname; private = %v, public = %v",
			bp.PrivateResources, bp.PublicResources)
	}
	if _, ok := bp.PublicResources["gw-demo-pub"]; ok {
		t.Error("both routes published the same hostname")
	}
	loser := verdictFor(t, verdicts, "demo", "pub")
	if loser.Accepted.Reason != ReasonDuplicateHostname {
		t.Errorf("reason = %q, want %q", loser.Accepted.Reason, ReasonDuplicateHostname)
	}
}

// A route is one or the other, so flipping visibility must not leave the
// resource behind in the section it came from.
func TestRenderPlacesARouteInExactlyOneSection(t *testing.T) {
	bp, _ := Render(defaultInputs(
		route("demo", "web", private()),
		route("demo", "api"),
	))

	if len(bp.PublicResources) != 1 || len(bp.PrivateResources) != 1 {
		t.Errorf("public = %v, private = %v, want one each",
			bp.PublicResources, bp.PrivateResources)
	}
	if _, ok := bp.PublicResources["gw-demo-web"]; ok {
		t.Error("the private route also reached public-resources")
	}
	if _, ok := bp.PrivateResources["gw-demo-api"]; ok {
		t.Error("the public route also reached private-resources")
	}
}

// Both sections are always present, as the "this controller owns nothing here"
// statement; an absent one would leave Pangolin's prefault in place.
func TestRenderAlwaysCarriesBothSections(t *testing.T) {
	bp, _ := Render(defaultInputs())

	if bp.PublicResources == nil {
		t.Error("public-resources is nil, want an empty object")
	}
	if bp.PrivateResources == nil {
		t.Error("private-resources is nil, want an empty object")
	}
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range want {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
