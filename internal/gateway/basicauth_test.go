// SPDX-License-Identifier: AGPL-3.0-only

package gateway

import (
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/p3l1/pangolin-gateway/internal/pangolin"
)

func withBasicAuth(in Inputs, ns, name string, r BasicAuthResult) Inputs {
	in.BasicAuth = map[types.NamespacedName]BasicAuthResult{
		{Namespace: ns, Name: name}: r,
	}
	return in
}

func TestRenderPutsBasicAuthOnAPublicResource(t *testing.T) {
	in := withBasicAuth(
		defaultInputs(route("demo", "web", withAnnotations(map[string]string{
			AnnotationBasicAuth: "true",
		}))),
		"demo", "web",
		BasicAuthResult{Credentials: &pangolin.BasicAuth{User: "web", Password: "s3cret"}},
	)

	bp, _ := Render(in)

	auth := bp.PublicResources["gw-demo-web"].Auth
	if auth == nil || auth.BasicAuth == nil {
		t.Fatalf("no basic auth on the resource: %+v", auth)
	}
	if auth.BasicAuth.User != "web" || auth.BasicAuth.Password != "s3cret" {
		t.Errorf("credentials = %q/%q, want web/s3cret",
			auth.BasicAuth.User, auth.BasicAuth.Password)
	}
	// Without it Pangolin answers an unauthenticated request with its login page
	// instead of a challenge, which no webhook client can act on.
	if !auth.BasicAuth.ExtendedCompatibility {
		t.Error("extendedCompatibility is off, so a non-browser client gets a login page")
	}
}

func TestRenderOmitsBasicAuthWhenUnannotated(t *testing.T) {
	bp, _ := Render(defaultInputs(route("demo", "web")))

	if auth := bp.PublicResources["gw-demo-web"].Auth; auth.BasicAuth != nil {
		t.Errorf("basic auth = %+v, want none", auth.BasicAuth)
	}
}

// Publishing the route without the protection it asked for would leave it open
// while its author believes it is not.
func TestRenderRejectsARouteWhoseCredentialsCouldNotBeResolved(t *testing.T) {
	in := withBasicAuth(
		defaultInputs(route("demo", "web", withAnnotations(map[string]string{
			AnnotationBasicAuth: "true",
		}))),
		"demo", "web",
		BasicAuthResult{Reason: "secret gw-web-basic-auth belongs to something else"},
	)

	bp, verdicts := Render(in)

	if _, published := bp.PublicResources["gw-demo-web"]; published {
		t.Error("route was published without the basic auth it asked for")
	}
	v := verdictFor(t, verdicts, "demo", "web")
	if !strings.Contains(v.Accepted.Message, "belongs to something else") {
		t.Errorf("message %q does not carry the reason", v.Accepted.Message)
	}
}

func TestRenderRejectsARouteWithNoCredentialsAtAll(t *testing.T) {
	in := defaultInputs(route("demo", "web", withAnnotations(map[string]string{
		AnnotationBasicAuth: "true",
	})))

	bp, verdicts := Render(in)

	if _, published := bp.PublicResources["gw-demo-web"]; published {
		t.Error("route was published without the basic auth it asked for")
	}
	if v := verdictFor(t, verdicts, "demo", "web"); v.Accepted.Status != metav1.ConditionFalse {
		t.Errorf("Accepted = %+v, want False", v.Accepted)
	}
}

func TestRenderRejectsAnUnreadableBasicAuthAnnotation(t *testing.T) {
	r := route("demo", "web", withAnnotations(map[string]string{
		AnnotationBasicAuth: "please",
	}))

	bp, verdicts := Render(defaultInputs(r))

	if _, published := bp.PublicResources["gw-demo-web"]; published {
		t.Error("route with an unreadable basic-auth annotation was published")
	}
	if v := verdictFor(t, verdicts, "demo", "web"); v.Accepted.Status != metav1.ConditionFalse {
		t.Errorf("Accepted = %+v, want False", v.Accepted)
	}
}

// The name is derived so no annotation can point the controller at a Secret it
// was never meant to read.
func TestBasicAuthSecretNameIsDerivedFromTheRoute(t *testing.T) {
	if got, want := BasicAuthSecretName("web"), "gw-web-basic-auth"; got != want {
		t.Errorf("BasicAuthSecretName = %q, want %q", got, want)
	}
}

func TestBasicAuthRequestedReadsTheAnnotation(t *testing.T) {
	for name, tc := range map[string]struct {
		annotations map[string]string
		want        bool
		wantErr     bool
	}{
		"absent":      {nil, false, false},
		"true":        {map[string]string{AnnotationBasicAuth: "true"}, true, false},
		"false":       {map[string]string{AnnotationBasicAuth: "false"}, false, false},
		"unreadable":  {map[string]string{AnnotationBasicAuth: "yes"}, false, true},
		"other route": {map[string]string{AnnotationSSO: "false"}, false, false},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := BasicAuthRequested(route("demo", "web", withAnnotations(tc.annotations)))
			if (err != nil) != tc.wantErr {
				t.Fatalf("error = %v, want error: %t", err, tc.wantErr)
			}
			if got != tc.want {
				t.Errorf("requested = %t, want %t", got, tc.want)
			}
		})
	}
}
