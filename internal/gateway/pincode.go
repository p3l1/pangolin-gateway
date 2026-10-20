// SPDX-License-Identifier: AGPL-3.0-only

package gateway

import (
	"fmt"
	"strconv"
	"strings"

	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// AnnotationPincode puts a six-digit PIN in front of a public resource, beside
// SSO rather than instead of it: either one lets a visitor through. An empty
// value asks for no PIN, so a template can always write the key.
const AnnotationPincode = "pangolin.p3l1.de/pincode"

// pincodeFor returns the PIN as Pangolin's schema takes it: a number between
// 100000 and 999999, which is why a leading zero is refused rather than sent.
func pincodeFor(r gatewayv1.HTTPRoute) (*int, error) {
	raw := strings.TrimSpace(r.Annotations[AnnotationPincode])
	if raw == "" {
		return nil, nil
	}

	pin, err := strconv.Atoi(raw)
	if err != nil || len(raw) != 6 || raw[0] < '1' || raw[0] > '9' {
		return nil, fmt.Errorf("annotation %s must be six digits not starting with 0, got %q",
			AnnotationPincode, raw)
	}
	return &pin, nil
}
