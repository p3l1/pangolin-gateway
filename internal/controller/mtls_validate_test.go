// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"
)

func caCertAndKey(t *testing.T) (certPEM, keyPEM string) {
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

func TestValidatedCAAcceptsASingleCertificate(t *testing.T) {
	cert, _ := caCertAndKey(t)
	res := validatedCA(cert)
	if res.Reason != "" || len(res.CACertificates) != 1 {
		t.Errorf("result = %+v, want one certificate and no reason", res)
	}
}

func TestValidatedCASplitsABundle(t *testing.T) {
	a, _ := caCertAndKey(t)
	b, _ := caCertAndKey(t)
	res := validatedCA(a + b)
	if res.Reason != "" || len(res.CACertificates) != 2 {
		t.Errorf("result = %+v, want two certificates from the bundle", res)
	}
}

func TestValidatedCAToleratesSurroundingWhitespace(t *testing.T) {
	cert, _ := caCertAndKey(t)
	res := validatedCA("\n  \r\n" + cert + "\n\n")
	if res.Reason != "" || len(res.CACertificates) != 1 {
		t.Errorf("result = %+v, want the certificate parsed despite whitespace", res)
	}
}

func TestValidatedCARejectsAPrivateKey(t *testing.T) {
	cert, key := caCertAndKey(t)
	if res := validatedCA(cert + key); res.Reason == "" {
		t.Error("a bundle containing a private key was accepted")
	}
}

func TestValidatedCARejectsMoreThanTwenty(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 21; i++ {
		c, _ := caCertAndKey(t)
		b.WriteString(c)
	}
	if res := validatedCA(b.String()); res.Reason == "" {
		t.Error("more than 20 certificates were accepted")
	}
}

func TestValidatedCARejectsInputWithoutACertificate(t *testing.T) {
	if res := validatedCA("not a pem block"); res.Reason == "" {
		t.Error("input without a certificate was accepted")
	}
}

func TestNormalizeFingerprint(t *testing.T) {
	if normalizeFingerprint("AB:CD:EF") != normalizeFingerprint("abcdef") {
		t.Error("a colon-separated uppercase fingerprint did not normalize to the plain hex")
	}
	if normalizeFingerprint(" ab:cd ") != "abcd" {
		t.Errorf("normalizeFingerprint = %q, want abcd", normalizeFingerprint(" ab:cd "))
	}
}
