// SPDX-License-Identifier: AGPL-3.0-only

package gateway

import (
	"reflect"
	"strings"
	"testing"
)

func TestRenderSendsTheEmailWhitelistBesideSSO(t *testing.T) {
	bp, _ := Render(defaultInputs(route("demo", "web", withAnnotations(map[string]string{
		AnnotationEmailWhitelist: "Anna@Example.com, bob@example.org\n anna@example.com\tcarol+tag@sub.example.net",
	}))))

	auth := bp.PublicResources["gw-demo-web"].Auth
	want := []string{"anna@example.com", "bob@example.org", "carol+tag@sub.example.net"}
	if !reflect.DeepEqual(auth.WhitelistUsers, want) {
		t.Errorf("whitelist-users = %v, want %v", auth.WhitelistUsers, want)
	}
	if !auth.SSOEnabled {
		t.Error("a whitelist switched SSO off; the sso annotation decides that")
	}
}

func TestRenderLeavesSSOToItsAnnotationWithAWhitelist(t *testing.T) {
	bp, _ := Render(defaultInputs(route("demo", "web", withAnnotations(map[string]string{
		AnnotationEmailWhitelist: "anna@example.com",
		AnnotationSSO:            "false",
	}))))

	auth := bp.PublicResources["gw-demo-web"].Auth
	if auth.SSOEnabled || len(auth.WhitelistUsers) != 1 {
		t.Errorf("auth = %+v, want SSO off and one whitelisted address", auth)
	}
}

func TestRenderTreatsAnEmptyEmailWhitelistAsNone(t *testing.T) {
	for name, kv := range map[string]map[string]string{
		"unannotated": nil,
		"empty":       {AnnotationEmailWhitelist: ""},
		"blank":       {AnnotationEmailWhitelist: "  \n"},
		"separators":  {AnnotationEmailWhitelist: " , ,"},
	} {
		t.Run(name, func(t *testing.T) {
			bp, _ := Render(defaultInputs(route("demo", "web", withAnnotations(kv))))
			res, ok := bp.PublicResources["gw-demo-web"]
			if !ok {
				t.Fatal("route was not published")
			}
			if len(res.Auth.WhitelistUsers) != 0 {
				t.Errorf("whitelist-users = %v, want none", res.Auth.WhitelistUsers)
			}
		})
	}
}

// Pangolin validates the list with zod and fails the whole apply for one entry
// it dislikes, so nothing short of a valid address may reach it.
func TestRenderRejectsAnEmailWhitelistPangolinWouldRefuse(t *testing.T) {
	for name, raw := range map[string]string{
		"no at sign":        "anna.example.com",
		"no domain dot":     "anna@localhost",
		"leading dot":       ".anna@example.com",
		"double dot":        "an..na@example.com",
		"double dot domain": "anna@example..com",
		"trailing dot":      "anna.@example.com",
		"short tld":         "anna@example.c",
		"underscore domain": "anna@exa_mple.com",
		"hyphen domain":     "anna@-example.com",
		"display name":      "Anna <anna@example.com>",
		"one bad of two":    "anna@example.com, nope",
	} {
		t.Run(name, func(t *testing.T) {
			bp, verdicts := Render(defaultInputs(route("demo", "web", withAnnotations(map[string]string{
				AnnotationEmailWhitelist: raw,
			}))))

			if _, published := bp.PublicResources["gw-demo-web"]; published {
				t.Fatal("route was published with a whitelist Pangolin refuses")
			}
			v := verdictFor(t, verdicts, "demo", "web")
			if v.Accepted.Status != "False" || v.Accepted.Reason != "UnsupportedValue" ||
				!strings.Contains(v.Accepted.Message, AnnotationEmailWhitelist) {
				t.Errorf("accepted = %+v, want UnsupportedValue naming the annotation", v.Accepted)
			}
		})
	}
}

func TestRenderPointsAWildcardAtResourcePolicy(t *testing.T) {
	bp, verdicts := Render(defaultInputs(route("demo", "web", withAnnotations(map[string]string{
		AnnotationEmailWhitelist: "anna@example.com,*@plan-b-chor.de",
	}))))

	if _, published := bp.PublicResources["gw-demo-web"]; published {
		t.Fatal("route was published with a wildcard Pangolin's resource schema refuses")
	}
	msg := verdictFor(t, verdicts, "demo", "web").Accepted.Message
	for _, want := range []string{"ResourcePolicy", "--pangolin-licensed", "*@plan-b-chor.de"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q does not mention %q", msg, want)
		}
	}
}

func TestRenderRefusesAnEmailWhitelistOnAPrivateRoute(t *testing.T) {
	bp, verdicts := Render(defaultInputs(route("demo", "web", withAnnotations(map[string]string{
		AnnotationVisibility:     VisibilityPrivate,
		AnnotationEmailWhitelist: "anna@example.com",
	}))))

	if _, published := bp.PrivateResources["gw-demo-web"]; published {
		t.Fatal("private route was published with a whitelist it cannot honour")
	}
	if msg := verdictFor(t, verdicts, "demo", "web").Accepted.Message; !strings.Contains(msg, AnnotationEmailWhitelist) {
		t.Errorf("message %q does not name the annotation", msg)
	}
}
