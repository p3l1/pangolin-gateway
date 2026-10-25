// SPDX-License-Identifier: AGPL-3.0-only

package gateway

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

func withMTLS(in Inputs, ns, name string, r MTLSResult) Inputs {
	in.MTLS = map[types.NamespacedName]MTLSResult{
		{Namespace: ns, Name: name}: r,
	}
	return in
}

func TestRenderPutsMtlsOnAPublicResource(t *testing.T) {
	in := withMTLS(
		defaultInputs(route("demo", "web", withAnnotations(map[string]string{
			AnnotationMTLSCASecret: "corp-ca",
		}))),
		"demo", "web",
		MTLSResult{CACertificates: []string{"PEM"}},
	)

	bp, _ := Render(in)

	auth := bp.PublicResources["gw-demo-web"].Auth
	if auth == nil || auth.MTLS == nil {
		t.Fatalf("no mTLS on the resource: %+v", auth)
	}
	if !auth.MTLS.Enabled || len(auth.MTLS.CACertificates) != 1 || auth.MTLS.CACertificates[0] != "PEM" {
		t.Errorf("mtls = %+v, want enabled with one CA", auth.MTLS)
	}
}

// Every public resource carries the block so dropping the annotation clears
// Pangolin's stored set; the empty slice must not be nil, or it would be omitted.
func TestRenderClearsMtlsWhenUnannotated(t *testing.T) {
	bp, _ := Render(defaultInputs(route("demo", "web")))

	mtls := bp.PublicResources["gw-demo-web"].Auth.MTLS
	if mtls == nil || mtls.Enabled || len(mtls.CACertificates) != 0 {
		t.Fatalf("mtls = %+v, want the disabled empty clear block", mtls)
	}
	if mtls.CACertificates == nil {
		t.Error("ca-certificates is nil, want an empty slice so the clear serializes")
	}
}

// Publishing the route without the mTLS it asked for would leave it open while
// its author believes a client certificate is required.
func TestRenderRejectsMtlsWhenCaUnavailable(t *testing.T) {
	in := withMTLS(
		defaultInputs(route("demo", "web", withAnnotations(map[string]string{
			AnnotationMTLSCASecret: "corp-ca",
		}))),
		"demo", "web",
		MTLSResult{Reason: "secret corp-ca not found"},
	)

	bp, verdicts := Render(in)

	if _, published := bp.PublicResources["gw-demo-web"]; published {
		t.Error("route was published without the mTLS CA it asked for")
	}
	v := verdictFor(t, verdicts, "demo", "web")
	if v.Accepted.Status != metav1.ConditionFalse ||
		v.Accepted.Reason != string(gatewayv1.RouteReasonUnsupportedValue) {
		t.Errorf("accepted = %+v, want False/UnsupportedValue", v.Accepted)
	}
}

func TestRenderRefusesMtlsOnAPrivateRoute(t *testing.T) {
	in := defaultInputs(route("demo", "web", withAnnotations(map[string]string{
		AnnotationVisibility:   VisibilityPrivate,
		AnnotationMTLSCASecret: "corp-ca",
	})))

	bp, verdicts := Render(in)

	if _, published := bp.PublicResources["gw-demo-web"]; published {
		t.Error("a private route was published as a public resource")
	}
	v := verdictFor(t, verdicts, "demo", "web")
	if v.Accepted.Status != metav1.ConditionFalse {
		t.Errorf("accepted = %+v, want the route refused", v.Accepted)
	}
}

func TestMTLSRequested(t *testing.T) {
	for _, a := range []string{AnnotationMTLSCA, AnnotationMTLSCASecret, AnnotationMTLSCAConfigMap} {
		if r := route("demo", "web", withAnnotations(map[string]string{a: "x"})); !MTLSRequested(r) {
			t.Errorf("MTLSRequested with %s = false, want true", a)
		}
	}
	if MTLSRequested(route("demo", "web")) {
		t.Error("MTLSRequested with no annotation = true, want false")
	}
}
