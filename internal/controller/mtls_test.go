// SPDX-License-Identifier: AGPL-3.0-only

package controller_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/p3l1/pangolin-gateway/internal/gateway"
	"github.com/p3l1/pangolin-gateway/internal/pangolin"
)

func mtlsCACert(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generating key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: "test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("creating certificate: %v", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func (h *harness) mtlsRoute(t *testing.T, name, hostname string, annotations map[string]string) {
	t.Helper()
	r := h.newRoute(name, hostname)
	r.Annotations = annotations
	h.create(t, r)
}

func (h *harness) publishedMTLS(t *testing.T) *pangolin.MTLS {
	t.Helper()
	res, ok := h.fake.Resources()["gw-demo-web"]
	if !ok {
		return nil
	}
	if res.Auth == nil || res.Auth.MTLS == nil {
		t.Fatalf("published without an mtls block: %+v", res.Auth)
	}
	return res.Auth.MTLS
}

func TestReconcileResolvesMtlsCaFromSecret(t *testing.T) {
	h := newHarness(t)
	h.setupClassAndGateway(t, "pangolin")
	h.create(t, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: h.namespace, Name: "corp-ca"},
		Data:       map[string][]byte{"ca.crt": []byte(mtlsCACert(t))},
	})
	h.mtlsRoute(t, "web", "web.example.com", map[string]string{gateway.AnnotationMTLSCASecret: "corp-ca"})

	h.mustReconcile(t)

	mtls := h.publishedMTLS(t)
	if mtls == nil {
		t.Fatal("route was not published")
	}
	if !mtls.Enabled || len(mtls.CACertificates) != 1 {
		t.Errorf("mtls = %+v, want enabled with one CA", mtls)
	}
}

func TestReconcileResolvesMtlsCaFromSecretTLSCrtFallback(t *testing.T) {
	h := newHarness(t)
	h.setupClassAndGateway(t, "pangolin")
	h.create(t, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: h.namespace, Name: "corp-ca"},
		Data:       map[string][]byte{"tls.crt": []byte(mtlsCACert(t))},
	})
	h.mtlsRoute(t, "web", "web.example.com", map[string]string{gateway.AnnotationMTLSCASecret: "corp-ca"})

	h.mustReconcile(t)

	if mtls := h.publishedMTLS(t); mtls == nil || !mtls.Enabled {
		t.Errorf("mtls = %+v, want the CA resolved from tls.crt", mtls)
	}
}

func TestReconcileResolvesMtlsCaFromConfigMapRootCrtFallback(t *testing.T) {
	h := newHarness(t)
	h.setupClassAndGateway(t, "pangolin")
	h.create(t, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: h.namespace, Name: "step-root"},
		Data:       map[string]string{"root_ca.crt": mtlsCACert(t)},
	})
	h.mtlsRoute(t, "web", "web.example.com", map[string]string{gateway.AnnotationMTLSCAConfigMap: "step-root"})

	h.mustReconcile(t)

	if mtls := h.publishedMTLS(t); mtls == nil || !mtls.Enabled {
		t.Errorf("mtls = %+v, want the CA resolved from a ConfigMap root_ca.crt", mtls)
	}
}

func TestReconcileResolvesMtlsCaFromInlineAnnotation(t *testing.T) {
	h := newHarness(t)
	h.setupClassAndGateway(t, "pangolin")
	h.mtlsRoute(t, "web", "web.example.com", map[string]string{gateway.AnnotationMTLSCA: mtlsCACert(t)})

	h.mustReconcile(t)

	if mtls := h.publishedMTLS(t); mtls == nil || !mtls.Enabled {
		t.Errorf("mtls = %+v, want the CA resolved from the inline annotation", mtls)
	}
}

func TestReconcileRejectsMtlsWhenSecretMissing(t *testing.T) {
	h := newHarness(t)
	h.setupClassAndGateway(t, "pangolin")
	h.mtlsRoute(t, "web", "web.example.com", map[string]string{gateway.AnnotationMTLSCASecret: "absent"})

	h.mustReconcile(t)

	if _, ok := h.fake.Resources()["gw-demo-web"]; ok {
		t.Error("route was published without the mTLS CA it asked for")
	}
	accepted := conditionOf(t, h.routeConditions(t, "web"), string(gatewayv1.RouteConditionAccepted))
	if accepted.Status != metav1.ConditionFalse {
		t.Errorf("accepted = %+v, want False", accepted)
	}
}

func TestReconcileRejectsMtlsWhenTwoSourcesSet(t *testing.T) {
	h := newHarness(t)
	h.setupClassAndGateway(t, "pangolin")
	h.mtlsRoute(t, "web", "web.example.com", map[string]string{
		gateway.AnnotationMTLSCA:       mtlsCACert(t),
		gateway.AnnotationMTLSCASecret: "corp-ca",
	})

	h.mustReconcile(t)

	if _, ok := h.fake.Resources()["gw-demo-web"]; ok {
		t.Error("route with two CA sources was published")
	}
}
