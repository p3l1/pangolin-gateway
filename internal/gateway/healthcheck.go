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

	// The strategy: http reads a response, tcp only opens a connection. A target
	// with no path that answers 2xx is described honestly by tcp and by nothing
	// else, so this annotation switches a check on by itself.
	AnnotationHealthcheckMode = "pangolin.p3l1.de/healthcheck-mode"

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

// healthcheckSwitches turn a check on; healthcheckTuning only shapes one that
// is already on, and httpOnlyHealthcheckSettings describe a response no TCP
// check ever reads.
var (
	healthcheckSwitches = []string{
		AnnotationHealthcheckPath,
		AnnotationHealthcheckMode,
	}
	healthcheckTuning = []string{
		AnnotationHealthcheckInterval,
		AnnotationHealthcheckTimeout,
		AnnotationHealthcheckStatus,
		AnnotationHealthcheckMethod,
		AnnotationHealthcheckFollowRedirects,
	}
	httpOnlyHealthcheckSettings = []string{
		AnnotationHealthcheckPath,
		AnnotationHealthcheckStatus,
		AnnotationHealthcheckMethod,
		AnnotationHealthcheckFollowRedirects,
	}
	healthcheckAnnotations = append(slices.Clone(healthcheckSwitches), healthcheckTuning...)
)

// healthcheckMethods are the verbs a check may use. Pangolin takes any string
// here and silently drops a check whose method is empty, so the value is
// checked before it can disable the check the route asked for.
var healthcheckMethods = []string{
	"DELETE", "GET", "HEAD", "OPTIONS", "PATCH", "POST", "PUT",
}

// healthcheckFor builds the target's check from its annotations. Returning nil
// without an error means no check was asked for.
func healthcheckFor(r gatewayv1.HTTPRoute, target pangolin.Target) (*pangolin.Healthcheck, error) {
	mode, err := healthcheckMode(r)
	if err != nil {
		return nil, err
	}
	path := r.Annotations[AnnotationHealthcheckPath]

	if mode == "" && path == "" {
		// Tuning without a switch yields no check at all, which is not what the
		// author expected; say so rather than publishing a route that lacks one.
		for _, a := range healthcheckTuning {
			if _, set := r.Annotations[a]; set {
				return nil, fmt.Errorf("%s is set without %s, so no healthcheck would be created",
					a, strings.Join(healthcheckSwitches, " or "))
			}
		}
		return nil, nil
	}
	if mode == "" {
		mode = pangolin.HealthcheckModeHTTP
	}

	interval, err := positiveSeconds(r, AnnotationHealthcheckInterval, DefaultHealthcheckInterval)
	if err != nil {
		return nil, err
	}
	timeout, err := positiveSeconds(r, AnnotationHealthcheckTimeout, DefaultHealthcheckTimeout)
	if err != nil {
		return nil, err
	}

	hc := &pangolin.Healthcheck{
		Hostname: target.Hostname,
		Port:     target.Port,
		Mode:     mode,
		Interval: interval,
		Timeout:  timeout,
	}
	if mode == pangolin.HealthcheckModeTCP {
		if err := refuseAnnotations(r, "a TCP healthcheck",
			"a TCP check opens a connection and reads no response",
			httpOnlyHealthcheckSettings...); err != nil {
			return nil, err
		}
		return hc, nil
	}

	if path == "" {
		return nil, fmt.Errorf("%s is %s, which needs %s to say what to request",
			AnnotationHealthcheckMode, pangolin.HealthcheckModeHTTP, AnnotationHealthcheckPath)
	}
	if !strings.HasPrefix(path, "/") {
		return nil, fmt.Errorf("annotation %s must start with \"/\", got %q",
			AnnotationHealthcheckPath, path)
	}

	hc.Path = path
	if hc.Method, err = healthcheckMethod(r); err != nil {
		return nil, err
	}
	if hc.Status, err = healthcheckStatus(r); err != nil {
		return nil, err
	}
	if hc.FollowRedirects, err = boolAnnotation(r, AnnotationHealthcheckFollowRedirects, true); err != nil {
		return nil, err
	}
	return hc, nil
}

// healthcheckMode returns the empty string when unannotated, which leaves the
// path to decide whether there is a check at all.
func healthcheckMode(r gatewayv1.HTTPRoute) (string, error) {
	raw, ok := r.Annotations[AnnotationHealthcheckMode]
	if !ok {
		return "", nil
	}
	switch raw {
	case pangolin.HealthcheckModeHTTP, pangolin.HealthcheckModeTCP:
		return raw, nil
	case "snmp", "icmp":
		return "", fmt.Errorf("annotation %s: Pangolin offers %q in its dashboard as a paid "+
			"feature its agent does not implement, so the check would silently run over HTTP",
			AnnotationHealthcheckMode, raw)
	default:
		return "", fmt.Errorf("annotation %s must be %q or %q, got %q",
			AnnotationHealthcheckMode, pangolin.HealthcheckModeHTTP, pangolin.HealthcheckModeTCP, raw)
	}
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
