// SPDX-License-Identifier: AGPL-3.0-only

package gateway

import (
	"fmt"

	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/p3l1/pangolin-gateway/internal/pangolin"
)

// The three CA sources for mTLS. Exactly one may be set: an inline PEM, a Secret,
// or a ConfigMap in the route's namespace. Reading a Secret or ConfigMap is I/O,
// done by the reconciler before the render; the PEM is the CA's public cert only.
const (
	AnnotationMTLSCA          = "pangolin.p3l1.de/mtls-ca"
	AnnotationMTLSCASecret    = "pangolin.p3l1.de/mtls-ca-secret"
	AnnotationMTLSCAConfigMap = "pangolin.p3l1.de/mtls-ca-configmap"
)

// mtlsAnnotations lists the CA-source annotations, so a private route can refuse
// them and the reconciler can tell a requesting route from a plain one.
var mtlsAnnotations = []string{AnnotationMTLSCA, AnnotationMTLSCASecret, AnnotationMTLSCAConfigMap}

// MTLSResult is what the reconciler resolved for one route. CACertificates and
// Reason are exclusive: a Reason says why the route cannot have the protection it
// asked for, which rejects it rather than publishing it open.
type MTLSResult struct {
	CACertificates []string
	Reason         string
}

// MTLSRequested reports whether the route names a CA source.
func MTLSRequested(r gatewayv1.HTTPRoute) bool {
	for _, a := range mtlsAnnotations {
		if r.Annotations[a] != "" {
			return true
		}
	}
	return false
}

// mtlsFor returns the mTLS block for a public resource, sent on every pass: a
// requesting route carries the resolved CAs, a plain one carries the disabled
// empty block that clears Pangolin's stored set.
func mtlsFor(r gatewayv1.HTTPRoute, in Inputs) (*pangolin.MTLS, error) {
	if !MTLSRequested(r) {
		return &pangolin.MTLS{Enabled: false, CACertificates: []string{}}, nil
	}

	result, ok := in.MTLS[nameOf(r)]
	switch {
	case !ok:
		return nil, fmt.Errorf("mTLS asked for, but no CA was resolved")
	case result.Reason != "":
		return nil, fmt.Errorf("mTLS CA is unavailable: %s", result.Reason)
	case len(result.CACertificates) == 0:
		return nil, fmt.Errorf("mTLS asked for, but the resolved CA set is empty")
	}
	return &pangolin.MTLS{Enabled: true, CACertificates: result.CACertificates}, nil
}
