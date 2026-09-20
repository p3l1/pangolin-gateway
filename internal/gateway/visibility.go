// SPDX-License-Identifier: AGPL-3.0-only

package gateway

import (
	"fmt"
	"strings"

	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// Annotations selecting which Pangolin section a route lands in, and who may
// reach it once it is private. Rationale: docs/private-resources.md.
const (
	// AnnotationVisibility switches a route from public-resources to
	// private-resources, which only a connected Pangolin client can reach.
	AnnotationVisibility = "pangolin.p3l1.de/visibility"

	// AnnotationRoles and AnnotationUsers are comma-separated. Without either,
	// only the organisation's admin role reaches the resource.
	AnnotationRoles = "pangolin.p3l1.de/roles"
	AnnotationUsers = "pangolin.p3l1.de/users"
)

// Values for AnnotationVisibility. Public is the default: a route that says
// nothing keeps behaving as it did before the annotation existed.
const (
	VisibilityPublic  = "public"
	VisibilityPrivate = "private"
)

func visibilityFor(r gatewayv1.HTTPRoute) (string, error) {
	v, ok := r.Annotations[AnnotationVisibility]
	if !ok || v == "" {
		return VisibilityPublic, nil
	}
	switch v {
	case VisibilityPublic, VisibilityPrivate:
		return v, nil
	default:
		return "", fmt.Errorf("annotation %s must be %q or %q, got %q",
			AnnotationVisibility, VisibilityPublic, VisibilityPrivate, v)
	}
}

// grantList reads a comma-separated annotation. An empty entry is rejected
// rather than dropped: a trailing comma usually means a name was deleted by
// hand, and silently granting one fewer role than the list reads is worse than
// saying so.
func grantList(r gatewayv1.HTTPRoute, annotation string) ([]string, error) {
	raw, ok := r.Annotations[annotation]
	if !ok {
		return nil, nil
	}
	if strings.TrimSpace(raw) == "" {
		return nil, fmt.Errorf("annotation %s is empty; remove it rather than leaving it blank",
			annotation)
	}

	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for i, part := range parts {
		entry := strings.TrimSpace(part)
		if entry == "" {
			return nil, fmt.Errorf("annotation %s has an empty entry at position %d: %q",
				annotation, i+1, raw)
		}
		out = append(out, entry)
	}
	return out, nil
}

// refuseAnnotations rejects the route when it carries an annotation the chosen
// visibility cannot honour. Ignoring one would publish something other than
// what the route says, which is the failure this controller is built to avoid.
func refuseAnnotations(
	r gatewayv1.HTTPRoute,
	subject, remedy string,
	annotations ...string,
) error {
	for _, a := range annotations {
		if _, set := r.Annotations[a]; set {
			return fmt.Errorf("annotation %s has no meaning on %s; %s", a, subject, remedy)
		}
	}
	return nil
}
