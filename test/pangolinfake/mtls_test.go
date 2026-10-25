// SPDX-License-Identifier: AGPL-3.0-only

package pangolinfake_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"

	"github.com/p3l1/pangolin-gateway/internal/pangolin"
)

// selfSignedCA returns a fresh self-signed CA as a certificate PEM and its
// private-key PEM, so each call produces a distinct fingerprint.
func selfSignedCA(t *testing.T) (certPEM, keyPEM string) {
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
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshalling key: %v", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
}

func mtlsResourceID(t *testing.T, c pangolin.Client, niceID string) int {
	t.Helper()
	resources, err := c.ListPublicResources(context.Background())
	if err != nil {
		t.Fatalf("listing resources: %v", err)
	}
	for _, r := range resources {
		if r.NiceID == niceID {
			return r.ResourceID
		}
	}
	t.Fatalf("resource %s not found in listing", niceID)
	return 0
}

func applyMTLS(t *testing.T, c pangolin.Client, cas []string) error {
	t.Helper()
	res := resource("mtls.example.com")
	res.Auth.MTLS = &pangolin.MTLS{Enabled: true, CACertificates: cas}
	return c.ApplyBlueprint(context.Background(), pangolin.Blueprint{
		PublicResources: map[string]pangolin.PublicResource{"gw-demo-mtls": res},
	})
}

func TestFakeStoresAndServesMtlsCertificates(t *testing.T) {
	_, c := newFake(t)
	caPEM, _ := selfSignedCA(t)

	if err := applyMTLS(t, c, []string{caPEM}); err != nil {
		t.Fatalf("apply: %v", err)
	}

	id := mtlsResourceID(t, c, "gw-demo-mtls")
	certs, err := c.ListResourceMtlsCertificates(context.Background(), id)
	if err != nil {
		t.Fatalf("listing mtls certificates: %v", err)
	}
	if len(certs) != 1 || certs[0].Fingerprint == "" {
		t.Errorf("certificates = %+v, want one with a fingerprint", certs)
	}
}

func TestFakeStripSimulatesOpenSourceBuild(t *testing.T) {
	f, c := newFake(t)
	f.SetMtlsStrip(true)
	caPEM, _ := selfSignedCA(t)

	if err := applyMTLS(t, c, []string{caPEM}); err != nil {
		t.Fatalf("apply: %v", err)
	}

	id := mtlsResourceID(t, c, "gw-demo-mtls")
	certs, err := c.ListResourceMtlsCertificates(context.Background(), id)
	if err != nil {
		t.Fatalf("listing mtls certificates: %v", err)
	}
	if len(certs) != 0 {
		t.Errorf("certificates = %+v, want none (strip simulates the open-source build)", certs)
	}
}

func TestFakeRejectsMtlsPrivateKey(t *testing.T) {
	_, c := newFake(t)
	_, keyPEM := selfSignedCA(t)

	if err := applyMTLS(t, c, []string{keyPEM}); err == nil {
		t.Fatal("want an error for a private key in ca-certificates")
	}
}

func TestFakeRejectsMoreThanTwentyMtlsCertificates(t *testing.T) {
	_, c := newFake(t)

	certs := make([]string, 21)
	for i := range certs {
		certs[i], _ = selfSignedCA(t)
	}
	if err := applyMTLS(t, c, certs); err == nil {
		t.Fatal("want an error for more than 20 certificates")
	}
}
