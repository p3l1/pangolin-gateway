// SPDX-License-Identifier: AGPL-3.0-only

package gateway

import (
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/p3l1/pangolin-gateway/internal/pangolin"
)

func withAccessRules(yaml string) routeOpt {
	return func(r *gatewayv1.HTTPRoute) {
		if r.Annotations == nil {
			r.Annotations = map[string]string{}
		}
		r.Annotations[AnnotationAccessRules] = yaml
	}
}

// rejectedRules renders one annotated route and returns the message it was
// rejected with, failing the test if the route was published instead.
func rejectedRules(t *testing.T, yaml string) string {
	t.Helper()

	bp, verdicts := Render(defaultInputs(route("demo", "web", withAccessRules(yaml))))

	if _, published := bp.PublicResources["gw-demo-web"]; published {
		t.Fatalf("route with access rules %q was published, want it rejected", yaml)
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

// The motivating case: a service behind SSO that must serve a few paths
// unauthenticated. Order is the contract, because Pangolin assigns priority by
// index and the first matching rule decides.
func TestRenderPublishesAccessRulesInOrder(t *testing.T) {
	r := route("demo", "web", withAccessRules(`
- action: allow
  match: path
  value: /script.js
- action: allow
  match: path
  value: /api/collect
- action: deny
  match: cidr
  value: 203.0.113.0/24
`))

	bp, verdicts := Render(defaultInputs(r))

	entry, ok := bp.PublicResources["gw-demo-web"]
	if !ok {
		t.Fatalf("route was not published; have %v", bp.PublicResources)
	}
	want := pangolin.Rules{
		{Action: pangolin.ActionAllow, Match: pangolin.MatchPath, Value: "/script.js"},
		{Action: pangolin.ActionAllow, Match: pangolin.MatchPath, Value: "/api/collect"},
		{Action: pangolin.ActionDeny, Match: pangolin.MatchCIDR, Value: "203.0.113.0/24"},
	}
	if len(entry.Rules) != len(want) {
		t.Fatalf("rules = %+v, want %+v", entry.Rules, want)
	}
	for i := range want {
		if entry.Rules[i] != want[i] {
			t.Errorf("rules[%d] = %+v, want %+v", i, entry.Rules[i], want[i])
		}
	}

	// SSO stays on: an allow rule opens the paths it names, not the resource.
	if entry.Auth == nil || !entry.Auth.SSOEnabled {
		t.Errorf("auth = %+v, want sso-enabled true", entry.Auth)
	}
	if v := verdictFor(t, verdicts, "demo", "web"); v.Accepted.Status != metav1.ConditionTrue {
		t.Errorf("Accepted = %+v, want True", v.Accepted)
	}
}

func TestRenderPublishesNoRulesWithoutTheAnnotation(t *testing.T) {
	bp, _ := Render(defaultInputs(route("demo", "web")))

	if got := bp.PublicResources["gw-demo-web"].Rules; len(got) != 0 {
		t.Errorf("rules = %+v, want none", got)
	}
}

// An empty list says "evaluate no rules", which is what removing the last rule
// from an existing route must produce.
func TestRenderAcceptsAnEmptyRuleList(t *testing.T) {
	for _, yaml := range []string{"[]", "\n", "   "} {
		bp, verdicts := Render(defaultInputs(route("demo", "web", withAccessRules(yaml))))

		if _, published := bp.PublicResources["gw-demo-web"]; !published {
			v := verdictFor(t, verdicts, "demo", "web")
			t.Errorf("route with access rules %q was rejected: %s", yaml, v.Accepted.Message)
		}
		if got := bp.PublicResources["gw-demo-web"].Rules; len(got) != 0 {
			t.Errorf("rules = %+v for %q, want none", got, yaml)
		}
	}
}

func TestRenderRejectsMalformedAccessRuleYAML(t *testing.T) {
	for name, yaml := range map[string]string{
		"not yaml":     "- action: allow\n   match: path\n  value: /x\n",
		"a mapping":    "action: allow\nmatch: path\nvalue: /x\n",
		"a scalar":     "allow",
		"a null entry": "- action: allow\n  match: path\n  value: /x\n- \n",
	} {
		t.Run(name, func(t *testing.T) {
			if msg := rejectedRules(t, yaml); !strings.Contains(msg, AnnotationAccessRules) {
				t.Errorf("message %q does not name the annotation", msg)
			}
		})
	}
}

// Someone will reach for priority. Ignoring it would silently produce an order
// they did not ask for, so say that the list order is the priority.
func TestRenderRejectsUnknownAccessRuleFields(t *testing.T) {
	msg := rejectedRules(t, "- action: allow\n  match: path\n  value: /x\n  priority: 5\n")

	if !strings.Contains(msg, "priority") {
		t.Errorf("message %q does not name the offending field", msg)
	}
	if !strings.Contains(msg, "order") {
		t.Errorf("message %q does not explain that list order carries priority", msg)
	}
}

func TestRenderRejectsAnUnknownActionOrMatch(t *testing.T) {
	for name, tc := range map[string]struct{ yaml, want string }{
		"action": {"- action: block\n  match: path\n  value: /x\n", "block"},
		"match":  {"- action: allow\n  match: header\n  value: /x\n", "header"},
	} {
		t.Run(name, func(t *testing.T) {
			if msg := rejectedRules(t, tc.yaml); !strings.Contains(msg, tc.want) {
				t.Errorf("message %q does not name %q", msg, tc.want)
			}
		})
	}
}

func TestRenderRejectsAnEmptyRuleValue(t *testing.T) {
	rejectedRules(t, "- action: allow\n  match: path\n  value: \"\"\n")
}

// Pangolin validates ip and cidr values itself and fails the entire apply on a
// bad one, taking every other route down with it. Reject the single route here.
func TestRenderRejectsMalformedAddressRules(t *testing.T) {
	for name, value := range map[string]string{
		"ip is a cidr":      "ip|203.0.113.0/24",
		"ip is a hostname":  "ip|web.example.com",
		"ip is nonsense":    "ip|203.0.113.999",
		"cidr has no mask":  "cidr|203.0.113.0",
		"cidr mask too big": "cidr|203.0.113.0/48",
		"cidr is nonsense":  "cidr|not-a-network",
	} {
		t.Run(name, func(t *testing.T) {
			match, v, _ := strings.Cut(value, "|")
			rejectedRules(t, "- action: deny\n  match: "+match+"\n  value: "+v+"\n")
		})
	}
}

func TestRenderAcceptsWellFormedAddressRules(t *testing.T) {
	for name, rule := range map[string]string{
		"ipv4":      "ip|203.0.113.5",
		"ipv6":      "ip|2001:db8::1",
		"cidr v4":   "cidr|203.0.113.0/24",
		"cidr v6":   "cidr|2001:db8::/32",
		"host cidr": "cidr|203.0.113.5/32",
	} {
		t.Run(name, func(t *testing.T) {
			match, v, _ := strings.Cut(rule, "|")
			r := route("demo", "web", withAccessRules(
				"- action: deny\n  match: "+match+"\n  value: "+v+"\n"))

			bp, verdicts := Render(defaultInputs(r))

			if _, published := bp.PublicResources["gw-demo-web"]; !published {
				t.Errorf("rejected: %s", verdictFor(t, verdicts, "demo", "web").Accepted.Message)
			}
		})
	}
}

// Pangolin runs its own glob validator over a path value and fails the whole
// apply when it objects, so the same rules are checked here first.
func TestRenderRejectsMalformedPathGlobs(t *testing.T) {
	for name, value := range map[string]string{
		"empty segment":    "/api//collect",
		"question mark":    "/item?",
		"space":            "/api/with space",
		"brace":            "/api/{id}",
		"truncated escape": "/api/%2",
		"non-hex escape":   "/api/%zz",
		"percent alone":    "/api/%",
		"square bracket":   "/api/[id]",
		// Doubled so YAML unescapes it to one backslash, which is what the glob
		// validator must see.
		"backslash":         `/api\\collect`,
		"just a slash pair": "//",
	} {
		t.Run(name, func(t *testing.T) {
			rejectedRules(t, "- action: allow\n  match: path\n  value: \""+value+"\"\n")
		})
	}
}

func TestRenderAcceptsWellFormedPathGlobs(t *testing.T) {
	for name, value := range map[string]string{
		"root":             "/",
		"exact file":       "/script.js",
		"segment wildcard": "/api/*",
		"partial wildcard": "/assets/*.js",
		"trailing slash":   "/api/",
		"no leading slash": "api/collect",
		"sub-delims":       "/a-b_c.d~e!$&'()+,;=@:",
		"percent escape":   "/api/%2Fcollect",
		"hash":             "/api#fragment",
	} {
		t.Run(name, func(t *testing.T) {
			r := route("demo", "web", withAccessRules(
				"- action: allow\n  match: path\n  value: \""+value+"\"\n"))

			bp, verdicts := Render(defaultInputs(r))

			if _, published := bp.PublicResources["gw-demo-web"]; !published {
				t.Errorf("path %q rejected: %s",
					value, verdictFor(t, verdicts, "demo", "web").Accepted.Message)
			}
		})
	}
}

// country, asn and region depend on MaxMind databases configured inside
// Pangolin, which no API reports. They pass through unchecked; a value Pangolin
// rejects fails the whole apply, which the README says out loud.
func TestRenderPassesGeographicMatchesThrough(t *testing.T) {
	for name, rule := range map[string]string{
		"country": "country|DE",
		"asn":     "asn|AS64496",
		"region":  "region|150",
		"all":     "country|ALL",
	} {
		t.Run(name, func(t *testing.T) {
			match, value, _ := strings.Cut(rule, "|")
			r := route("demo", "web", withAccessRules(
				"- action: deny\n  match: "+match+"\n  value: "+value+"\n"))

			bp, verdicts := Render(defaultInputs(r))

			entry, published := bp.PublicResources["gw-demo-web"]
			if !published {
				t.Fatalf("rejected: %s", verdictFor(t, verdicts, "demo", "web").Accepted.Message)
			}
			if len(entry.Rules) != 1 || entry.Rules[0].Value != value {
				t.Errorf("rules = %+v, want the value passed through verbatim", entry.Rules)
			}
		})
	}
}

// A region id or an ASN written as a bare YAML number is a string on the wire;
// Pangolin coerces it, and rejecting it here would be a needless difference.
func TestRenderAcceptsANumericRuleValue(t *testing.T) {
	r := route("demo", "web", withAccessRules("- action: deny\n  match: region\n  value: 150\n"))

	bp, verdicts := Render(defaultInputs(r))

	entry, published := bp.PublicResources["gw-demo-web"]
	if !published {
		t.Fatalf("rejected: %s", verdictFor(t, verdicts, "demo", "web").Accepted.Message)
	}
	if len(entry.Rules) != 1 || entry.Rules[0].Value != "150" {
		t.Errorf("rules = %+v, want value \"150\"", entry.Rules)
	}
}

// The apply is all or nothing, which is the whole reason the renderer validates:
// one route's bad rule must not unpublish its neighbours.
func TestRenderRejectsOnlyTheRouteWithABadRule(t *testing.T) {
	bad := route("demo", "web", withAccessRules("- action: deny\n  match: ip\n  value: nope\n"))
	good := route("demo", "api", withAccessRules("- action: allow\n  match: path\n  value: /ping\n"))

	bp, verdicts := Render(defaultInputs(bad, good))

	if _, published := bp.PublicResources["gw-demo-web"]; published {
		t.Error("the route with a malformed rule was published")
	}
	if _, published := bp.PublicResources["gw-demo-api"]; !published {
		t.Errorf("the neighbouring route was dropped: %s",
			verdictFor(t, verdicts, "demo", "api").Accepted.Message)
	}
}

// Every rejection names its rule, or an author with a long list cannot find it.
func TestRenderNamesTheOffendingRule(t *testing.T) {
	msg := rejectedRules(t, `
- action: allow
  match: path
  value: /ok
- action: deny
  match: ip
  value: not-an-ip
`)

	if !strings.Contains(msg, "[1]") {
		t.Errorf("message %q does not name the index of the offending rule", msg)
	}
}
