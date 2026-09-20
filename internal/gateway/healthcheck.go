// SPDX-License-Identifier: AGPL-3.0-only

package gateway

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/p3l1/pangolin-gateway/internal/pangolin"
)

// Annotations describing the target's healthcheck. A path switches the check
// on; every other one only tunes it. Hostname and port are not annotations: a
// check against an address other than the target's would not describe it.
const (
	AnnotationHealthcheckPath     = "pangolin.p3l1.de/healthcheck-path"
	AnnotationHealthcheckInterval = "pangolin.p3l1.de/healthcheck-interval"
	AnnotationHealthcheckTimeout  = "pangolin.p3l1.de/healthcheck-timeout"

	// The status a healthy target answers with. Without it Pangolin accepts any
	// 2xx, which no endpoint whose only route answers 4xx can ever satisfy.
	AnnotationHealthcheckStatus = "pangolin.p3l1.de/healthcheck-status"

	AnnotationHealthcheckMethod = "pangolin.p3l1.de/healthcheck-method"

	// Pangolin follows redirects unless told otherwise, which compares the
	// expected status against whatever the redirect lands on.
	AnnotationHealthcheckFollowRedirects = "pangolin.p3l1.de/healthcheck-follow-redirects"
)

// Pangolin's own defaults, restated so the blueprint says what it means.
const (
	DefaultHealthcheckInterval = 30
	DefaultHealthcheckTimeout  = 5
	DefaultHealthcheckMethod   = "GET"
)

// healthcheckTuning is every annotation that needs a path to mean anything.
var healthcheckTuning = []string{
	AnnotationHealthcheckInterval,
	AnnotationHealthcheckTimeout,
	AnnotationHealthcheckStatus,
	AnnotationHealthcheckMethod,
	AnnotationHealthcheckFollowRedirects,
}

// healthcheckMethods are the verbs a check may use. Pangolin takes any string
// here and silently drops a check whose method is empty, so the value is
// checked before it can disable the check the route asked for.
var healthcheckMethods = []string{
	"DELETE", "GET", "HEAD", "OPTIONS", "PATCH", "POST", "PUT",
}

// healthcheckFor builds the target's check from its annotations. Returning nil
// without an error means no check was asked for.
func healthcheckFor(r gatewayv1.HTTPRoute, target pangolin.Target) (*pangolin.Healthcheck, error) {
	path := r.Annotations[AnnotationHealthcheckPath]

	if path == "" {
		// Tuning without a path yields no check at all, which is not what the
		// author expected; say so rather than publishing a route that lacks one.
		for _, a := range healthcheckTuning {
			if _, set := r.Annotations[a]; set {
				return nil, fmt.Errorf("%s is set without %s, so no healthcheck would be created",
					a, AnnotationHealthcheckPath)
			}
		}
		return nil, nil
	}
	if !strings.HasPrefix(path, "/") {
		return nil, fmt.Errorf("annotation %s must start with \"/\", got %q",
			AnnotationHealthcheckPath, path)
	}

	interval, err := positiveSeconds(r, AnnotationHealthcheckInterval, DefaultHealthcheckInterval)
	if err != nil {
		return nil, err
	}
	timeout, err := positiveSeconds(r, AnnotationHealthcheckTimeout, DefaultHealthcheckTimeout)
	if err != nil {
		return nil, err
	}
	method, err := healthcheckMethod(r)
	if err != nil {
		return nil, err
	}
	status, err := healthcheckStatus(r)
	if err != nil {
		return nil, err
	}
	followRedirects, err := boolAnnotation(r, AnnotationHealthcheckFollowRedirects, true)
	if err != nil {
		return nil, err
	}

	return &pangolin.Healthcheck{
		Hostname:        target.Hostname,
		Port:            target.Port,
		Path:            path,
		Interval:        interval,
		Timeout:         timeout,
		Method:          method,
		Status:          status,
		FollowRedirects: followRedirects,
	}, nil
}

func healthcheckMethod(r gatewayv1.HTTPRoute) (string, error) {
	raw, ok := r.Annotations[AnnotationHealthcheckMethod]
	if !ok {
		return DefaultHealthcheckMethod, nil
	}
	method := strings.ToUpper(strings.TrimSpace(raw))
	if !slices.Contains(healthcheckMethods, method) {
		return "", fmt.Errorf("annotation %s must be one of %s, got %q",
			AnnotationHealthcheckMethod, strings.Join(healthcheckMethods, ", "), raw)
	}
	return method, nil
}

// healthcheckStatus returns zero when unannotated, which leaves Pangolin at its
// own default of accepting any 2xx.
func healthcheckStatus(r gatewayv1.HTTPRoute) (int, error) {
	raw, ok := r.Annotations[AnnotationHealthcheckStatus]
	if !ok {
		return 0, nil
	}
	code, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || code < 100 || code > 599 {
		return 0, fmt.Errorf("annotation %s must be an HTTP status code between 100 and 599, got %q",
			AnnotationHealthcheckStatus, raw)
	}
	return code, nil
}

func positiveSeconds(r gatewayv1.HTTPRoute, annotation string, fallback int) (int, error) {
	raw, ok := r.Annotations[annotation]
	if !ok || raw == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("annotation %s must be a whole number of seconds, got %q",
			annotation, raw)
	}
	if n <= 0 {
		return 0, fmt.Errorf("annotation %s must be greater than zero, got %d", annotation, n)
	}
	return n, nil
}
