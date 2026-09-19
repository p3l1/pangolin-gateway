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

func TestBlueprintWithNoResourcesStillMarshalsTheKey(t *testing.T) {
	raw, err := json.Marshal(Blueprint{PublicResources: map[string]PublicResource{}})
	if err != nil {
		t.Fatalf("marshalling blueprint: %v", err)
	}

	// An absent key would leave Pangolin's prefault in place; an empty object is
	// the explicit "this controller currently owns nothing" statement.
	if got, want := string(raw), `{"public-resources":{}}`; got != want {
		t.Errorf("empty blueprint = %s, want %s", got, want)
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
					Hostname: "web.demo.svc.cluster.local",
					Port:     8080,
					Path:     "/healthz",
					Interval: 30,
					Timeout:  5,
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
		"hostname": "web.demo.svc.cluster.local",
		"port":     float64(8080),
		"path":     "/healthz",
		"interval": float64(30),
		"timeout":  float64(5),
	} {
		if got := hc[key]; got != want {
			t.Errorf("healthcheck[%q] = %v, want %v", key, got, want)
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
