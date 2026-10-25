// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/p3l1/pangolin-gateway/internal/gateway"
	"github.com/p3l1/pangolin-gateway/internal/pangolin"
)

// maxMtlsCertificates is the trusted-CA cap Pangolin enforces per resource. The
// controller checks it up front so one over-long bundle cannot fail the whole
// transactional apply.
const maxMtlsCertificates = 20

// +kubebuilder:rbac:groups="",resources=secrets,verbs=get
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get

// resolveMTLS returns the trusted CAs for every served route that named a
// source. An error aborts the pass: a route whose CA is momentarily unreadable
// must not be rendered without it, which would publish it open.
func (p *Publisher) resolveMTLS(
	ctx context.Context,
	routes []gatewayv1.HTTPRoute,
) (map[types.NamespacedName]gateway.MTLSResult, error) {
	var resolved map[types.NamespacedName]gateway.MTLSResult

	for _, r := range routes {
		if !gateway.MTLSRequested(r) {
			continue
		}
		result, err := p.mtlsFor(ctx, r)
		if err != nil {
			return nil, err
		}
		if resolved == nil {
			resolved = map[types.NamespacedName]gateway.MTLSResult{}
		}
		resolved[types.NamespacedName{Namespace: r.Namespace, Name: r.Name}] = result
	}
	return resolved, nil
}

func (p *Publisher) mtlsFor(
	ctx context.Context,
	r gatewayv1.HTTPRoute,
) (gateway.MTLSResult, error) {
	inline := r.Annotations[gateway.AnnotationMTLSCA]
	secretName := r.Annotations[gateway.AnnotationMTLSCASecret]
	configMapName := r.Annotations[gateway.AnnotationMTLSCAConfigMap]

	set := 0
	for _, v := range []string{inline, secretName, configMapName} {
		if v != "" {
			set++
		}
	}
	if set > 1 {
		return mtlsRefused("set only one of %s, %s or %s",
			gateway.AnnotationMTLSCA, gateway.AnnotationMTLSCASecret, gateway.AnnotationMTLSCAConfigMap), nil
	}

	switch {
	case secretName != "":
		return p.caFromSecret(ctx, r.Namespace, secretName)
	case configMapName != "":
		return p.caFromConfigMap(ctx, r.Namespace, configMapName)
	default:
		return validatedCA(inline), nil
	}
}

// caFromSecret reads the CA public certificate from a Secret, preferring ca.crt
// and falling back to tls.crt, so both a cert-manager CA Secret and an issued
// cert's Secret work. The key is what matters, not the Secret type.
func (p *Publisher) caFromSecret(
	ctx context.Context,
	namespace, name string,
) (gateway.MTLSResult, error) {
	var secret corev1.Secret
	err := p.Client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &secret)
	switch {
	case apierrors.IsNotFound(err):
		return mtlsRefused("Secret %s/%s not found", namespace, name), nil
	case err != nil:
		return gateway.MTLSResult{}, fmt.Errorf("reading Secret %s/%s: %w", namespace, name, err)
	}

	for _, key := range []string{"ca.crt", "tls.crt"} {
		if raw := secret.Data[key]; len(raw) > 0 {
			return validatedCA(string(raw)), nil
		}
	}
	return mtlsRefused("Secret %s/%s has no ca.crt or tls.crt key", namespace, name), nil
}

// caFromConfigMap reads the CA public certificate from a ConfigMap, preferring
// ca.crt and falling back to root_ca.crt, which is step-ca's default key.
func (p *Publisher) caFromConfigMap(
	ctx context.Context,
	namespace, name string,
) (gateway.MTLSResult, error) {
	var cm corev1.ConfigMap
	err := p.Client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &cm)
	switch {
	case apierrors.IsNotFound(err):
		return mtlsRefused("ConfigMap %s/%s not found", namespace, name), nil
	case err != nil:
		return gateway.MTLSResult{}, fmt.Errorf("reading ConfigMap %s/%s: %w", namespace, name, err)
	}

	for _, key := range []string{"ca.crt", "root_ca.crt"} {
		if raw := cm.Data[key]; raw != "" {
			return validatedCA(raw), nil
		}
	}
	return mtlsRefused("ConfigMap %s/%s has no ca.crt or root_ca.crt key", namespace, name), nil
}

// validatedCA parses a PEM value into individual certificate PEMs. It rejects a
// private key, an unparseable certificate, an empty set, or more than Pangolin
// allows, so a bad CA is caught before the transactional apply.
func validatedCA(raw string) gateway.MTLSResult {
	var certs []string
	rest := []byte(raw)
	for {
		block, next := pem.Decode(rest)
		if block == nil {
			break
		}
		rest = next
		if strings.Contains(block.Type, "PRIVATE KEY") {
			return gateway.MTLSResult{Reason: "the CA value contains a private key; provide the public certificate only"}
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		if _, err := x509.ParseCertificate(block.Bytes); err != nil {
			return gateway.MTLSResult{Reason: fmt.Sprintf("the CA value has an unparseable certificate: %v", err)}
		}
		certs = append(certs, string(pem.EncodeToMemory(block)))
	}

	switch {
	case len(certs) == 0:
		return gateway.MTLSResult{Reason: "the CA value has no PEM certificate"}
	case len(certs) > maxMtlsCertificates:
		return gateway.MTLSResult{Reason: fmt.Sprintf(
			"the CA value has %d certificates, more than the %d Pangolin allows", len(certs), maxMtlsCertificates)}
	}
	return gateway.MTLSResult{CACertificates: certs}
}

func mtlsRefused(format string, args ...any) gateway.MTLSResult {
	return gateway.MTLSResult{Reason: fmt.Sprintf(format, args...)}
}

// verifyMTLS confirms, after a successful apply, that each published mTLS route's
// trusted CA set actually took effect. A set that is empty, mismatched, or
// unreadable flips that route alone to MtlsNotApplied, so the control is never
// reported active when it is not — notably on an open-source build that strips
// auth.mtls. It does not abort the pass or disturb other routes.
func (p *Publisher) verifyMTLS(
	ctx context.Context,
	mtls map[types.NamespacedName]gateway.MTLSResult,
	verdicts map[types.NamespacedName]gateway.Verdict,
) {
	want := map[types.NamespacedName]map[string]bool{}
	for nn, v := range verdicts {
		if !v.Published() {
			continue
		}
		res, ok := mtls[nn]
		if !ok || len(res.CACertificates) == 0 {
			continue
		}
		fps := map[string]bool{}
		for _, cert := range res.CACertificates {
			if fp := fingerprintOf(cert); fp != "" {
				fps[fp] = true
			}
		}
		want[nn] = fps
	}
	if len(want) == 0 {
		return
	}

	byNice, listErr := p.publicResourceIDs(ctx)
	for nn := range want {
		id, ok := byNice[verdicts[nn].Key]
		if listErr != nil || !ok {
			markMtlsNotApplied(verdicts, nn,
				"could not confirm the mTLS CA was applied: the resource was not found in the listing")
			continue
		}
		got, err := p.Pangolin.ListResourceMtlsCertificates(ctx, id)
		if err != nil || !coversFingerprints(got, want[nn]) {
			markMtlsNotApplied(verdicts, nn,
				"the mTLS CA set did not take effect: the Pangolin build may not be Enterprise/SaaS, "+
					"or the API key may lack the listResourceMtlsCertificates action")
		}
	}
}

func (p *Publisher) publicResourceIDs(ctx context.Context) (map[string]int, error) {
	resources, err := p.Pangolin.ListPublicResources(ctx)
	if err != nil {
		return nil, err
	}
	byNice := make(map[string]int, len(resources))
	for _, r := range resources {
		byNice[r.NiceID] = r.ResourceID
	}
	return byNice, nil
}

func markMtlsNotApplied(
	verdicts map[types.NamespacedName]gateway.Verdict,
	nn types.NamespacedName,
	msg string,
) {
	v, ok := verdicts[nn]
	if !ok || !v.Published() {
		return
	}
	v.Accepted = gateway.ConditionResult{
		Status:  metav1.ConditionFalse,
		Reason:  gateway.ReasonMtlsNotApplied,
		Message: msg,
	}
	verdicts[nn] = v
}

// coversFingerprints reports whether the instance holds every desired CA, by
// SHA-256 fingerprint. Both sides are normalized so a colon-separated or
// uppercase form from Pangolin still matches.
func coversFingerprints(got []pangolin.MtlsCertificate, want map[string]bool) bool {
	have := make(map[string]bool, len(got))
	for _, c := range got {
		have[normalizeFingerprint(c.Fingerprint)] = true
	}
	for fp := range want {
		if !have[fp] {
			return false
		}
	}
	return true
}

func fingerprintOf(certPEM string) string {
	block, _ := pem.Decode([]byte(certPEM))
	if block == nil || block.Type != "CERTIFICATE" {
		return ""
	}
	sum := sha256.Sum256(block.Bytes)
	return hex.EncodeToString(sum[:])
}

func normalizeFingerprint(s string) string {
	return strings.ToLower(strings.NewReplacer(":", "", " ", "", "\t", "", "\n", "").Replace(s))
}
