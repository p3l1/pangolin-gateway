// SPDX-License-Identifier: AGPL-3.0-only

package pangolin

import (
	"encoding/json"
	"strings"
	"testing"
)

// The wire format is the contract with Pangolin, so these tests assert the exact
// key spellings its blueprint schema expects rather than Go field names.
func TestBlueprintMarshalsToPangolinKeys(t *testing.T) {
	bp := Blueprint{PublicResources: map[string]PublicResource{
		"gw-demo-web": {
			Name:       "demo/web",
			Mode:       ModeHTTP,
			FullDomain: "demo.example.com",
			Auth:       &Auth{SSOEnabled: true},
			Targets: []Target{{
				Site:     "my-site",
				Method:   MethodHTTP,
				Hostname: "web.demo.svc.cluster.local",
				Port:     8080,
			}},
		},
	}}

	raw, err := json.Marshal(bp)
	if err != nil {
		t.Fatalf("marshalling blueprint: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshalling blueprint: %v", err)
	}

	resources, ok := got["public-resources"].(map[string]any)
	if !ok {
		t.Fatalf("no public-resources object in %s", raw)
	}
	entry, ok := resources["gw-demo-web"].(map[string]any)
	if !ok {
		t.Fatalf("no gw-demo-web entry in %s", raw)
	}

	for key, want := range map[string]any{
		"name":        "demo/web",
		"mode":        "http",
		"full-domain": "demo.example.com",
	} {
		if got := entry[key]; got != want {
			t.Errorf("entry[%q] = %v, want %v", key, got, want)
		}
	}

	auth, ok := entry["auth"].(map[string]any)
	if !ok {
		t.Fatalf("no auth object in %s", raw)
	}
	if got := auth["sso-enabled"]; got != true {
		t.Errorf("auth[sso-enabled] = %v, want true", got)
	}

	targets, ok := entry["targets"].([]any)
	if !ok || len(targets) != 1 {
		t.Fatalf("targets = %v, want one element", entry["targets"])
	}
	target := targets[0].(map[string]any)
	for key, want := range map[string]any{
		"site":     "my-site",
		"method":   "http",
		"hostname": "web.demo.svc.cluster.local",
		"port":     float64(8080),
	} {
		if got := target[key]; got != want {
			t.Errorf("target[%q] = %v, want %v", key, got, want)
		}
	}
}

// Pangolin rejects a "protocol" key alongside "mode" in some validation paths,
// and an empty auth object is not the same as an absent one.
func TestBlueprintOmitsUnsetOptionalFields(t *testing.T) {
	bp := Blueprint{PublicResources: map[string]PublicResource{
		"gw-demo-web": {Name: "demo/web", Mode: ModeHTTP, FullDomain: "demo.example.com"},
	}}

	raw, err := json.Marshal(bp)
	if err != nil {
		t.Fatalf("marshalling blueprint: %v", err)
	}

	var got map[string]map[string]map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshalling blueprint: %v", err)
	}
	entry := got["public-resources"]["gw-demo-web"]

	for _, key := range []string{"protocol", "auth", "targets", "proxy-port"} {
		if _, present := entry[key]; present {
			t.Errorf("entry carries %q, want it omitted when unset", key)
		}
	}
}

func TestHealthcheckMarshalsToPangolinKeys(t *testing.T) {
	bp := Blueprint{PublicResources: map[string]PublicResource{
		"gw-demo-web": {
			Name: "demo/web", Mode: ModeHTTP, FullDomain: "demo.example.com",
			Targets: []Target{{
				Hostname: "web.demo.svc.cluster.local",
				Port:     8080,
				Healthcheck: &Healthcheck{
					Hostname:        "web.demo.svc.cluster.local",
					Port:            8080,
					Mode:            HealthcheckModeHTTP,
					Path:            "/healthz",
					Interval:        30,
					Timeout:         5,
					Method:          "GET",
					Status:          400,
					FollowRedirects: true,
				},
			}},
		},
	}}

	raw, err := json.Marshal(bp)
	if err != nil {
		t.Fatalf("marshalling blueprint: %v", err)
	}

	var got map[string]map[string]map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshalling blueprint: %v", err)
	}
	targets := got["public-resources"]["gw-demo-web"]["targets"].([]any)
	hc, ok := targets[0].(map[string]any)["healthcheck"].(map[string]any)
	if !ok {
		t.Fatalf("no healthcheck object in %s", raw)
	}

	for key, want := range map[string]any{
		"hostname":         "web.demo.svc.cluster.local",
		"port":             float64(8080),
		"mode":             HealthcheckModeHTTP,
		"path":             "/healthz",
		"interval":         float64(30),
		"timeout":          float64(5),
		"method":           "GET",
		"status":           float64(400),
		"follow-redirects": true,
	} {
		if got := hc[key]; got != want {
			t.Errorf("healthcheck[%q] = %v, want %v", key, got, want)
		}
	}
}

// Pangolin skips a column its document leaves out, so a key that vanishes with
// its annotation would keep the value the route no longer asks for.
func TestHealthcheckKeepsEveryKeyItCanClear(t *testing.T) {
	raw, err := json.Marshal(Healthcheck{Hostname: "web", Port: 80, Path: "/healthz"})
	if err != nil {
		t.Fatalf("marshalling healthcheck: %v", err)
	}
	for _, key := range []string{"mode", "path", "method", "status", "follow-redirects"} {
		if !strings.Contains(string(raw), `"`+key+`"`) {
			t.Errorf("healthcheck omits %q when unset: %s", key, raw)
		}
	}
}

func TestTargetOmitsAnAbsentHealthcheck(t *testing.T) {
	raw, err := json.Marshal(Target{Hostname: "web", Port: 80})
	if err != nil {
		t.Fatalf("marshalling target: %v", err)
	}
	if strings.Contains(string(raw), "healthcheck") {
		t.Errorf("target carries a healthcheck key when unset: %s", raw)
	}
}

func TestRulesMarshalToPangolinKeys(t *testing.T) {
	bp := Blueprint{PublicResources: map[string]PublicResource{
		"gw-demo-web": {
			Name: "demo/web", Mode: ModeHTTP, FullDomain: "demo.example.com",
			Rules: Rules{
				{Action: ActionAllow, Match: MatchPath, Value: "/script.js"},
				{Action: ActionDeny, Match: MatchCIDR, Value: "203.0.113.0/24"},
			},
		},
	}}

	raw, err := json.Marshal(bp)
	if err != nil {
		t.Fatalf("marshalling blueprint: %v", err)
	}

	var got map[string]map[string]map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshalling blueprint: %v", err)
	}

	rules, ok := got["public-resources"]["gw-demo-web"]["rules"].([]any)
	if !ok || len(rules) != 2 {
		t.Fatalf("rules = %v, want two entries", got["public-resources"]["gw-demo-web"]["rules"])
	}

	first := rules[0].(map[string]any)
	for key, want := range map[string]any{
		"action": "allow",
		"match":  "path",
		"value":  "/script.js",
	} {
		if got := first[key]; got != want {
			t.Errorf("rules[0][%q] = %v, want %v", key, got, want)
		}
	}

	// Order is the contract: Pangolin assigns priority by index, and the first
	// rule matching a request wins.
	if got := rules[1].(map[string]any)["value"]; got != "203.0.113.0/24" {
		t.Errorf("rules[1][value] = %v, want the second rule; order was not preserved", got)
	}
}

// Priority and enabled are Pangolin's fields, not this controller's: order
// carries priority, and a rule that should not apply is removed.
func TestRulesCarryNoPriorityOrEnabledKey(t *testing.T) {
	raw, err := json.Marshal(Rule{Action: ActionAllow, Match: MatchPath, Value: "/"})
	if err != nil {
		t.Fatalf("marshalling rule: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshalling rule: %v", err)
	}
	for _, key := range []string{"priority", "enabled"} {
		if _, present := got[key]; present {
			t.Errorf("rule carries %q, want it omitted", key)
		}
	}
}

// Pangolin derives applyRules from whether rules is a non-empty array, and its
// schema accepts an array or nothing — never null. A nil slice marshalling to
// null would fail the whole apply, and an absent key would leave rule
// evaluation switched on after the last rule was removed.
func TestRulesMarshalAsAnEmptyArrayWhenUnset(t *testing.T) {
	raw, err := json.Marshal(PublicResource{Name: "demo/web", Mode: ModeHTTP})
	if err != nil {
		t.Fatalf("marshalling resource: %v", err)
	}

	var got map[string]json.RawMessage
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshalling resource: %v", err)
	}

	rules, present := got["rules"]
	if !present {
		t.Fatalf("resource carries no rules key: %s", raw)
	}
	if string(rules) != "[]" {
		t.Errorf("rules = %s, want []", rules)
	}
}

func TestPrivateResourceMarshalsToPangolinKeys(t *testing.T) {
	bp := Blueprint{PrivateResources: map[string]PrivateResource{
		"gw-demo-web": {
			Name:            "demo/web",
			Mode:            ModeHTTP,
			Sites:           []string{"my-site"},
			Destination:     "web.demo.svc.cluster.local",
			DestinationPort: 8080,
			FullDomain:      "demo.example.com",
			Scheme:          SchemeHTTP,
			Roles:           []string{"Member"},
			Users:           []string{"alice@example.com"},
		},
	}}

	raw, err := json.Marshal(bp)
	if err != nil {
		t.Fatalf("marshalling blueprint: %v", err)
	}

	var got map[string]map[string]map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshalling blueprint: %v", err)
	}
	entry, ok := got["private-resources"]["gw-demo-web"]
	if !ok {
		t.Fatalf("no private-resources entry in %s", raw)
	}

	for key, want := range map[string]any{
		"name":             "demo/web",
		"mode":             "http",
		"destination":      "web.demo.svc.cluster.local",
		"destination-port": float64(8080),
		"full-domain":      "demo.example.com",
		"scheme":           "http",
	} {
		if got := entry[key]; got != want {
			t.Errorf("entry[%q] = %v, want %v", key, got, want)
		}
	}

	// The plural key: Pangolin deprecated the singular "site".
	sites, ok := entry["sites"].([]any)
	if !ok || len(sites) != 1 || sites[0] != "my-site" {
		t.Errorf("sites = %v, want [my-site]", entry["sites"])
	}
	if _, present := entry["site"]; present {
		t.Error("entry carries the deprecated site key")
	}
}

// tcp-ports, udp-ports and disable-icmp are forced by Pangolin for http mode,
// and auth has no counterpart on a private resource at all.
func TestPrivateResourceOmitsWhatHTTPModeDoesNotUse(t *testing.T) {
	raw, err := json.Marshal(PrivateResource{Name: "demo/web", Mode: ModeHTTP})
	if err != nil {
		t.Fatalf("marshalling resource: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshalling resource: %v", err)
	}
	for _, key := range []string{
		"tcp-ports", "udp-ports", "disable-icmp", "auth", "targets", "rules",
		"roles", "users", "alias", "ssl",
	} {
		if _, present := got[key]; present {
			t.Errorf("resource carries %q, want it omitted when unset", key)
		}
	}
}

// An absent section would leave Pangolin's prefault in place; an empty object is
// the explicit "this controller currently owns nothing here".
func TestBlueprintCarriesBothSectionsWhenEmpty(t *testing.T) {
	raw, err := json.Marshal(Blueprint{
		PublicResources:  map[string]PublicResource{},
		PrivateResources: map[string]PrivateResource{},
	})
	if err != nil {
		t.Fatalf("marshalling blueprint: %v", err)
	}
	if got, want := string(raw), `{"public-resources":{},"private-resources":{}}`; got != want {
		t.Errorf("empty blueprint = %s, want %s", got, want)
	}
}
