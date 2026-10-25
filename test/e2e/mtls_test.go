// SPDX-License-Identifier: AGPL-3.0-only

package e2e

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/p3l1/pangolin-gateway/internal/gateway"
)

func e2eMtlsCACert(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generating key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: "e2e-ca"},
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

// A ConfigMap source exercises the chart's configmaps:get RBAC in-cluster — the
// one permission this feature adds — and the step-ca-style root_ca.crt key.
func TestMtlsCaIsPublishedInTheCluster(t *testing.T) {
	c := newClient(t)
	ctx := context.Background()

	waitForDeployment(t, c, deploymentName)
	waitForDeployment(t, c, fakeDeployment)

	gw := &gatewayv1.Gateway{}
	gw.Namespace = namespace
	gw.Name = "e2e-mtls-gw"
	gw.Spec.GatewayClassName = "pangolin"
	gw.Spec.Listeners = []gatewayv1.Listener{{
		Name: "http", Port: 80, Protocol: gatewayv1.HTTPProtocolType,
	}}
	if err := c.Create(ctx, gw); err != nil {
		t.Fatalf("creating Gateway: %v", err)
	}
	t.Cleanup(func() { _ = c.Delete(context.Background(), gw) })

	cm := &corev1.ConfigMap{}
	cm.Namespace = namespace
	cm.Name = "e2e-mtls-ca"
	cm.Data = map[string]string{"root_ca.crt": e2eMtlsCACert(t)}
	if err := c.Create(ctx, cm); err != nil {
		t.Fatalf("creating ConfigMap: %v", err)
	}
	t.Cleanup(func() { _ = c.Delete(context.Background(), cm) })

	port := gatewayv1.PortNumber(8080)
	route := &gatewayv1.HTTPRoute{}
	route.Namespace = namespace
	route.Name = "e2e-mtls"
	route.Annotations = map[string]string{gateway.AnnotationMTLSCAConfigMap: "e2e-mtls-ca"}
	route.Spec.ParentRefs = []gatewayv1.ParentReference{{Name: "e2e-mtls-gw"}}
	route.Spec.Hostnames = []gatewayv1.Hostname{"e2e-mtls.example.com"}
	route.Spec.Rules = []gatewayv1.HTTPRouteRule{{
		BackendRefs: []gatewayv1.HTTPBackendRef{{
			BackendRef: gatewayv1.BackendRef{
				BackendObjectReference: gatewayv1.BackendObjectReference{
					Name: "e2e-mtls", Port: &port,
				},
			},
		}},
	}}
	if err := c.Create(ctx, route); err != nil {
		t.Fatalf("creating HTTPRoute: %v", err)
	}
	t.Cleanup(func() { _ = c.Delete(context.Background(), route) })

	eachPoll(t, readyTimeout, func() error {
		state := readFakeState(t)
		entry, ok := state.Resources["gw-"+namespace+"-e2e-mtls"]
		if !ok {
			return fmt.Errorf("route not published yet; fake holds %v", keysOf(state.Resources))
		}
		auth, ok := entry["auth"].(map[string]any)
		if !ok {
			return fmt.Errorf("no auth block in %v", entry)
		}
		mtls, ok := auth["mtls"].(map[string]any)
		if !ok {
			return fmt.Errorf("no mtls block in %v", auth)
		}
		if mtls["enabled"] != true {
			return fmt.Errorf("mtls not enabled: %v", mtls)
		}
		cas, ok := mtls["ca-certificates"].([]any)
		if !ok || len(cas) == 0 {
			return fmt.Errorf("no ca-certificates published: %v", mtls)
		}
		return nil
	})
}
