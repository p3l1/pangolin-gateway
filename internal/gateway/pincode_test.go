// SPDX-License-Identifier: AGPL-3.0-only

package gateway

import (
	"strings"
	"testing"
)

func TestRenderPutsThePincodeBesideSSO(t *testing.T) {
	bp, _ := Render(defaultInputs(route("demo", "web", withAnnotations(map[string]string{
		AnnotationPincode: "482913",
	}))))

	auth := bp.PublicResources["gw-demo-web"].Auth
	if auth.Pincode == nil || *auth.Pincode != 482913 {
		t.Fatalf("pincode = %v, want 482913", auth.Pincode)
	}
	if !auth.SSOEnabled {
		t.Error("a PIN switched SSO off; both must let a visitor through")
	}
}

// A template writes the key whether or not it has a PIN to put there.
func TestRenderTreatsAnEmptyPincodeAsNone(t *testing.T) {
	for name, kv := range map[string]map[string]string{
		"unannotated": nil,
		"empty":       {AnnotationPincode: ""},
		"blank":       {AnnotationPincode: "  "},
	} {
		t.Run(name, func(t *testing.T) {
			bp, _ := Render(defaultInputs(route("demo", "web", withAnnotations(kv))))
			res, ok := bp.PublicResources["gw-demo-web"]
			if !ok {
				t.Fatal("route was not published")
			}
			if res.Auth.Pincode != nil {
				t.Errorf("pincode = %d, want none", *res.Auth.Pincode)
			}
		})
	}
}

// Pangolin accepts 100000 to 999999 only, and an apply it refuses unpublishes
// every other route with it.
func TestRenderRejectsAPincodePangolinWouldRefuse(t *testing.T) {
	for _, raw := range []string{"12345", "1234567", "012345", "48291a", "+48291", "-48291", "4829 1"} {
		t.Run(raw, func(t *testing.T) {
			in := defaultInputs(route("demo", "web", withAnnotations(map[string]string{
				AnnotationPincode: raw,
			})))

			bp, verdicts := Render(in)

			if _, published := bp.PublicResources["gw-demo-web"]; published {
				t.Fatal("route was published with a PIN Pangolin refuses")
			}
			v := verdictFor(t, verdicts, "demo", "web")
			if !strings.Contains(v.Accepted.Message, AnnotationPincode) {
				t.Errorf("message %q does not name the annotation", v.Accepted.Message)
			}
		})
	}
}
