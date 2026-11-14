// SPDX-License-Identifier: AGPL-3.0-only

package gateway

import (
	"fmt"
	"strings"
	"unicode/utf8"

	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/p3l1/pangolin-gateway/internal/pangolin"
)

// The maintenance page replaces a public resource with Pangolin's own card.
// The mode switches it on and then needs a title and a message; text without
// a mode is kept but shows nothing.
const (
	AnnotationMaintenanceMode          = "pangolin.p3l1.de/maintenance-mode"
	AnnotationMaintenanceTitle         = "pangolin.p3l1.de/maintenance-title"
	AnnotationMaintenanceMessage       = "pangolin.p3l1.de/maintenance-message"
	AnnotationMaintenanceEstimatedTime = "pangolin.p3l1.de/maintenance-estimated-time"
)

// Values for AnnotationMaintenanceMode. Automatic shows the page only while no
// target is healthy, so it relies on the route's healthcheck.
const (
	MaintenanceAutomatic = "automatic"
	MaintenanceForced    = "forced"
)

var maintenanceAnnotations = []string{
	AnnotationMaintenanceMode, AnnotationMaintenanceTitle,
	AnnotationMaintenanceMessage, AnnotationMaintenanceEstimatedTime,
}

// Character limits of Pangolin's schema, which fails the whole apply beyond them.
var maintenanceLimits = map[string]int{
	AnnotationMaintenanceTitle:         255,
	AnnotationMaintenanceMessage:       2000,
	AnnotationMaintenanceEstimatedTime: 100,
}

// maintenanceFor returns the block to send: nil when unlicensed, since Pangolin
// would drop it, and {enabled:false} when the page is off, since an absent block
// leaves the stored one in place.
func maintenanceFor(r gatewayv1.HTTPRoute, licensed bool) (*pangolin.Maintenance, error) {
	values := map[string]string{}
	for _, a := range maintenanceAnnotations {
		if v := strings.TrimSpace(r.Annotations[a]); v != "" {
			values[a] = v
		}
	}

	if !licensed {
		for _, a := range maintenanceAnnotations {
			if values[a] != "" {
				return nil, fmt.Errorf("annotation %s needs the maintenance page, a licensed Pangolin "+
					"feature; start the controller with --pangolin-licensed", a)
			}
		}
		return nil, nil
	}

	for _, a := range maintenanceAnnotations {
		if limit, ok := maintenanceLimits[a]; ok && utf8.RuneCountInString(values[a]) > limit {
			return nil, fmt.Errorf("annotation %s is longer than %d characters", a, limit)
		}
	}

	mode := values[AnnotationMaintenanceMode]
	switch mode {
	case "":
		return &pangolin.Maintenance{}, nil
	case MaintenanceAutomatic, MaintenanceForced:
	default:
		return nil, fmt.Errorf("annotation %s must be %q or %q, got %q",
			AnnotationMaintenanceMode, MaintenanceAutomatic, MaintenanceForced, mode)
	}
	for _, a := range []string{AnnotationMaintenanceTitle, AnnotationMaintenanceMessage} {
		if values[a] == "" {
			return nil, fmt.Errorf("annotation %s is required while %s is set",
				a, AnnotationMaintenanceMode)
		}
	}

	return &pangolin.Maintenance{
		Enabled:       true,
		Type:          mode,
		Title:         values[AnnotationMaintenanceTitle],
		Message:       values[AnnotationMaintenanceMessage],
		EstimatedTime: values[AnnotationMaintenanceEstimatedTime],
	}, nil
}
