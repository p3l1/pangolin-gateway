// SPDX-License-Identifier: AGPL-3.0-only

package gateway

import (
	"fmt"

	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/p3l1/pangolin-gateway/internal/pangolin"
)

// AnnotationBasicAuth puts HTTP basic auth in front of a public resource. It
// carries no credentials: the repositories that hold these routes are public,
// and a password written into an annotation is a published password.
const AnnotationBasicAuth = "pangolin.p3l1.de/basic-auth"

// BasicAuthSecretSuffix names the Secret holding a route's credentials. The
// name is derived rather than annotated, so no route can point the controller
// at a Secret it was never meant to read.
const BasicAuthSecretSuffix = "-basic-auth"

// BasicAuthResult is what the reconciler resolved for one route. Credentials
// and Reason are exclusive: a Reason says why the route cannot have the
// protection it asked for, which rejects it rather than publishing it open.
type BasicAuthResult struct {
	Credentials *pangolin.BasicAuth
	Reason      string
}

func BasicAuthSecretName(routeName string) string {
	return KeyPrefix + routeName + BasicAuthSecretSuffix
}

// BasicAuthRequested reports whether the route asks to be protected. The
// reconciler reads it to decide which Secrets to ensure before rendering.
func BasicAuthRequested(r gatewayv1.HTTPRoute) (bool, error) {
	return boolAnnotation(r, AnnotationBasicAuth, false)
}

// basicAuthFor returns the credentials the reconciler resolved for this route.
// A nil result without an error means the route asked for no protection.
func basicAuthFor(r gatewayv1.HTTPRoute, in Inputs) (*pangolin.BasicAuth, error) {
	requested, err := BasicAuthRequested(r)
	if err != nil || !requested {
		return nil, err
	}

	result, ok := in.BasicAuth[nameOf(r)]
	switch {
	case !ok:
		return nil, fmt.Errorf("annotation %s asks for basic auth, but no credentials were resolved",
			AnnotationBasicAuth)
	case result.Credentials == nil:
		return nil, fmt.Errorf("basic auth credentials are unavailable: %s", result.Reason)
	}

	credentials := *result.Credentials
	credentials.ExtendedCompatibility = true
	return &credentials, nil
}
