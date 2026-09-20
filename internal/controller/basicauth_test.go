// SPDX-License-Identifier: AGPL-3.0-only

package controller_test

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/p3l1/pangolin-gateway/internal/gateway"
)

func (h *harness) basicAuthSecret(t *testing.T, routeName string) corev1.Secret {
	t.Helper()

	var secret corev1.Secret
	name := types.NamespacedName{
		Namespace: h.namespace,
		Name:      gateway.BasicAuthSecretName(routeName),
	}
	if err := h.client.Get(h.ctx, name, &secret); err != nil {
		t.Fatalf("reading Secret %s: %v", name, err)
	}
	return secret
}

func (h *harness) basicAuthRoute(t *testing.T, name, hostname string) {
	t.Helper()

	r := h.newRoute(name, hostname)
	r.Annotations = map[string]string{gateway.AnnotationBasicAuth: "true"}
	h.create(t, r)
}

// The credential is generated rather than annotated: the repositories holding
// these routes are public, so an annotated password is a published one.
func TestReconcileGeneratesTheBasicAuthCredential(t *testing.T) {
	h := newHarness(t)
	h.setupClassAndGateway(t, "pangolin")
	h.basicAuthRoute(t, "web", "web.example.com")

	h.mustReconcile(t)

	secret := h.basicAuthSecret(t, "web")
	user, password := string(secret.Data["username"]), string(secret.Data["password"])
	if user != "web" || password == "" {
		t.Fatalf("secret holds %q/%q, want a username and a generated password", user, password)
	}
	if secret.Type != corev1.SecretTypeBasicAuth {
		t.Errorf("secret type = %q, want %q", secret.Type, corev1.SecretTypeBasicAuth)
	}

	// Owned by the route, so Kubernetes removes it with the route and the
	// controller needs no permission to delete one.
	if len(secret.OwnerReferences) != 1 || secret.OwnerReferences[0].Kind != "HTTPRoute" {
		t.Fatalf("owner references = %+v, want one HTTPRoute", secret.OwnerReferences)
	}

	resource, ok := h.fake.Resources()["gw-demo-web"]
	if !ok {
		t.Fatal("route was not published")
	}
	if resource.Auth == nil || resource.Auth.BasicAuth == nil {
		t.Fatalf("published without basic auth: %+v", resource.Auth)
	}
	if resource.Auth.BasicAuth.User != user || resource.Auth.BasicAuth.Password != password {
		t.Errorf("published %q/%q, want the Secret's %q/%q",
			resource.Auth.BasicAuth.User, resource.Auth.BasicAuth.Password, user, password)
	}
}

// A password rewritten on the next pass would break the URL an operator has
// already copied into a webhook.
func TestReconcileKeepsAnExistingBasicAuthCredential(t *testing.T) {
	h := newHarness(t)
	h.setupClassAndGateway(t, "pangolin")
	h.basicAuthRoute(t, "web", "web.example.com")

	h.mustReconcile(t)
	first := string(h.basicAuthSecret(t, "web").Data["password"])
	h.mustReconcile(t)
	second := string(h.basicAuthSecret(t, "web").Data["password"])

	if first != second {
		t.Errorf("password changed between passes: %q then %q", first, second)
	}
}

// Adopting a Secret the controller does not own would turn whatever it holds
// into the password of a resource on the internet.
func TestReconcileRefusesToAdoptAForeignSecret(t *testing.T) {
	h := newHarness(t)
	h.setupClassAndGateway(t, "pangolin")
	h.create(t, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: h.namespace,
			Name:      gateway.BasicAuthSecretName("web"),
		},
		StringData: map[string]string{"username": "someone", "password": "else"},
	})
	h.basicAuthRoute(t, "web", "web.example.com")

	h.mustReconcile(t)

	if _, published := h.fake.Resources()["gw-demo-web"]; published {
		t.Error("route was published with a Secret the controller does not own")
	}
	accepted := conditionOf(t, h.routeConditions(t, "web"), "Accepted")
	if accepted.Status != metav1.ConditionFalse {
		t.Errorf("Accepted = %+v, want False", accepted)
	}
	if !strings.Contains(accepted.Message, "not owned by this route") {
		t.Errorf("message %q does not say the Secret belongs to something else", accepted.Message)
	}
}

// A rehearsal that writes a Secret is not a rehearsal.
func TestReconcileCreatesNoSecretOnADryRun(t *testing.T) {
	h := newHarness(t)
	h.publisher.DryRun = true
	h.setupClassAndGateway(t, "pangolin")
	h.basicAuthRoute(t, "web", "web.example.com")

	h.mustReconcile(t)

	var secret corev1.Secret
	name := types.NamespacedName{
		Namespace: h.namespace,
		Name:      gateway.BasicAuthSecretName("web"),
	}
	if err := h.client.Get(h.ctx, name, &secret); err == nil {
		t.Errorf("dry run created Secret %s", name)
	}
	accepted := conditionOf(t, h.routeConditions(t, "web"), "Accepted")
	if !strings.Contains(accepted.Message, "would be created") {
		t.Errorf("message %q does not say the Secret would be created", accepted.Message)
	}
}

// A route on another controller's class is none of this controller's business,
// and a Secret for one would be a write nobody asked for.
func TestReconcileGeneratesNoCredentialForAForeignRoute(t *testing.T) {
	h := newHarness(t)
	h.setupClassAndGateway(t, "pangolin")
	h.create(t, &gatewayv1.GatewayClass{
		ObjectMeta: metav1.ObjectMeta{Name: "other"},
		Spec:       gatewayv1.GatewayClassSpec{ControllerName: "example.com/other"},
	})
	h.create(t, &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Namespace: h.namespace, Name: "other-gw"},
		Spec: gatewayv1.GatewaySpec{
			GatewayClassName: "other",
			Listeners: []gatewayv1.Listener{{
				Name: "http", Port: 80, Protocol: gatewayv1.HTTPProtocolType,
			}},
		},
	})
	r := h.newRoute("foreign", "foreign.example.com")
	r.Spec.ParentRefs = []gatewayv1.ParentReference{{Name: "other-gw"}}
	r.Annotations = map[string]string{gateway.AnnotationBasicAuth: "true"}
	h.create(t, r)

	h.mustReconcile(t)

	var secret corev1.Secret
	name := types.NamespacedName{
		Namespace: h.namespace,
		Name:      gateway.BasicAuthSecretName("foreign"),
	}
	if err := h.client.Get(h.ctx, name, &secret); err == nil {
		t.Errorf("created Secret %s for a route on another controller's class", name)
	}
}
