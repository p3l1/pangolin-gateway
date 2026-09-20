// SPDX-License-Identifier: AGPL-3.0-only

package gateway

import (
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/p3l1/pangolin-gateway/internal/pangolin"
)

func TestRenderAddsAHealthcheckWhenAPathIsGiven(t *testing.T) {
	r := route("demo", "web", withAnnotations(map[string]string{
		AnnotationHealthcheckPath: "/healthz",
	}))

	bp, _ := Render(defaultInputs(r))

	target := bp.PublicResources["gw-demo-web"].Targets[0]
	if target.Healthcheck == nil {
		t.Fatal("no healthcheck on the target")
	}
	hc := target.Healthcheck
	if got, want := hc.Path, "/healthz"; got != want {
		t.Errorf("path = %q, want %q", got, want)
	}
	// A check against a different address would not describe this target.
	if hc.Hostname != target.Hostname || hc.Port != target.Port {
		t.Errorf("healthcheck addresses %s:%d, want the target's %s:%d",
			hc.Hostname, hc.Port, target.Hostname, target.Port)
	}
	if got, want := hc.Interval, DefaultHealthcheckInterval; got != want {
		t.Errorf("interval = %d, want the default %d", got, want)
	}
	if got, want := hc.Timeout, DefaultHealthcheckTimeout; got != want {
		t.Errorf("timeout = %d, want the default %d", got, want)
	}
}

func TestRenderHonoursHealthcheckIntervalAndTimeout(t *testing.T) {
	r := route("demo", "web", withAnnotations(map[string]string{
		AnnotationHealthcheckPath:     "/healthz",
		AnnotationHealthcheckInterval: "15",
		AnnotationHealthcheckTimeout:  "3",
	}))

	bp, _ := Render(defaultInputs(r))

	hc := bp.PublicResources["gw-demo-web"].Targets[0].Healthcheck
	if hc == nil {
		t.Fatal("no healthcheck on the target")
	}
	if hc.Interval != 15 || hc.Timeout != 3 {
		t.Errorf("interval/timeout = %d/%d, want 15/3", hc.Interval, hc.Timeout)
	}
}

// Without a path there is nothing to check, so the field stays absent rather
// than defaulting to "/" — a check nobody asked for is a surprise.
func TestRenderOmitsTheHealthcheckWithoutAPath(t *testing.T) {
	bp, _ := Render(defaultInputs(route("demo", "web")))

	if hc := bp.PublicResources["gw-demo-web"].Targets[0].Healthcheck; hc != nil {
		t.Errorf("healthcheck = %+v, want none when no path is annotated", hc)
	}
}

// Interval and timeout without a path are almost certainly a mistake — the
// author expected a check and will not get one.
func TestRenderRejectsHealthcheckSettingsWithoutAPath(t *testing.T) {
	for name, annotations := range map[string]map[string]string{
		"interval only": {AnnotationHealthcheckInterval: "15"},
		"timeout only":  {AnnotationHealthcheckTimeout: "3"},
	} {
		t.Run(name, func(t *testing.T) {
			bp, verdicts := Render(defaultInputs(route("demo", "web", withAnnotations(annotations))))

			if _, published := bp.PublicResources["gw-demo-web"]; published {
				t.Error("route was published with healthcheck settings that do nothing")
			}
			v := verdictFor(t, verdicts, "demo", "web")
			if v.Accepted.Reason != string(gatewayv1.RouteReasonUnsupportedValue) {
				t.Errorf("reason = %q, want UnsupportedValue", v.Accepted.Reason)
			}
		})
	}
}

func TestRenderRejectsMalformedHealthcheckNumbers(t *testing.T) {
	for name, annotations := range map[string]map[string]string{
		"interval not a number": {
			AnnotationHealthcheckPath:     "/healthz",
			AnnotationHealthcheckInterval: "often",
		},
		"timeout not a number": {
			AnnotationHealthcheckPath:    "/healthz",
			AnnotationHealthcheckTimeout: "quick",
		},
		"interval zero": {
			AnnotationHealthcheckPath:     "/healthz",
			AnnotationHealthcheckInterval: "0",
		},
		"timeout negative": {
			AnnotationHealthcheckPath:    "/healthz",
			AnnotationHealthcheckTimeout: "-1",
		},
	} {
		t.Run(name, func(t *testing.T) {
			bp, verdicts := Render(defaultInputs(route("demo", "web", withAnnotations(annotations))))

			if _, published := bp.PublicResources["gw-demo-web"]; published {
				t.Error("route with a malformed healthcheck setting was published")
			}
			if v := verdictFor(t, verdicts, "demo", "web"); v.Accepted.Status != metav1.ConditionFalse {
				t.Errorf("Accepted = %+v, want False", v.Accepted)
			}
		})
	}
}

// A path that is not a path would reach Pangolin and fail the whole apply.
func TestRenderRejectsAHealthcheckPathWithoutASlash(t *testing.T) {
	r := route("demo", "web", withAnnotations(map[string]string{
		AnnotationHealthcheckPath: "healthz",
	}))

	bp, verdicts := Render(defaultInputs(r))

	if _, published := bp.PublicResources["gw-demo-web"]; published {
		t.Error("route with a relative healthcheck path was published")
	}
	if v := verdictFor(t, verdicts, "demo", "web"); v.Accepted.Status != metav1.ConditionFalse {
		t.Errorf("Accepted = %+v, want False", v.Accepted)
	}
}

// The endpoint this was built for answers 400 on its only route, so a check
// that insists on a 2xx would be permanently red.
func TestRenderHonoursAnExpectedHealthcheckStatus(t *testing.T) {
	r := route("demo", "web", withAnnotations(map[string]string{
		AnnotationHealthcheckPath:   "/api/webhook",
		AnnotationHealthcheckStatus: "400",
	}))

	bp, _ := Render(defaultInputs(r))

	hc := bp.PublicResources["gw-demo-web"].Targets[0].Healthcheck
	if hc == nil {
		t.Fatal("no healthcheck on the target")
	}
	if got, want := hc.Status, 400; got != want {
		t.Errorf("status = %d, want %d", got, want)
	}
}

// Zero is how the blueprint says "any 2xx", which is Pangolin's own default.
func TestRenderLeavesTheExpectedStatusAtZeroWhenUnannotated(t *testing.T) {
	r := route("demo", "web", withAnnotations(map[string]string{
		AnnotationHealthcheckPath: "/healthz",
	}))

	bp, _ := Render(defaultInputs(r))

	if got := bp.PublicResources["gw-demo-web"].Targets[0].Healthcheck.Status; got != 0 {
		t.Errorf("status = %d, want zero for any 2xx", got)
	}
}

func TestRenderRejectsAnImpossibleHealthcheckStatus(t *testing.T) {
	for name, value := range map[string]string{
		"not a number": "teapot",
		"below 100":    "99",
		"above 599":    "600",
		"negative":     "-200",
	} {
		t.Run(name, func(t *testing.T) {
			r := route("demo", "web", withAnnotations(map[string]string{
				AnnotationHealthcheckPath:   "/healthz",
				AnnotationHealthcheckStatus: value,
			}))

			bp, verdicts := Render(defaultInputs(r))

			if _, published := bp.PublicResources["gw-demo-web"]; published {
				t.Error("route with an impossible expected status was published")
			}
			if v := verdictFor(t, verdicts, "demo", "web"); v.Accepted.Status != metav1.ConditionFalse {
				t.Errorf("Accepted = %+v, want False", v.Accepted)
			}
		})
	}
}

func TestRenderHonoursAHealthcheckMethod(t *testing.T) {
	r := route("demo", "web", withAnnotations(map[string]string{
		AnnotationHealthcheckPath:   "/healthz",
		AnnotationHealthcheckMethod: "head",
	}))

	bp, _ := Render(defaultInputs(r))

	hc := bp.PublicResources["gw-demo-web"].Targets[0].Healthcheck
	if hc == nil {
		t.Fatal("no healthcheck on the target")
	}
	if got, want := hc.Method, "HEAD"; got != want {
		t.Errorf("method = %q, want %q", got, want)
	}
}

// Pangolin discards a check whose method is empty before it ever runs, so the
// method is always sent rather than left to a default nobody can see.
func TestRenderSendsAHealthcheckMethodByDefault(t *testing.T) {
	r := route("demo", "web", withAnnotations(map[string]string{
		AnnotationHealthcheckPath: "/healthz",
	}))

	bp, _ := Render(defaultInputs(r))

	if got, want := bp.PublicResources["gw-demo-web"].Targets[0].Healthcheck.Method,
		DefaultHealthcheckMethod; got != want {
		t.Errorf("method = %q, want %q", got, want)
	}
}

func TestRenderRejectsAnUnknownHealthcheckMethod(t *testing.T) {
	for name, value := range map[string]string{
		"not a verb": "FETCH",
		"empty":      "",
	} {
		t.Run(name, func(t *testing.T) {
			r := route("demo", "web", withAnnotations(map[string]string{
				AnnotationHealthcheckPath:   "/healthz",
				AnnotationHealthcheckMethod: value,
			}))

			bp, verdicts := Render(defaultInputs(r))

			if _, published := bp.PublicResources["gw-demo-web"]; published {
				t.Error("route with an unknown healthcheck method was published")
			}
			if v := verdictFor(t, verdicts, "demo", "web"); v.Accepted.Status != metav1.ConditionFalse {
				t.Errorf("Accepted = %+v, want False", v.Accepted)
			}
		})
	}
}

// Pangolin follows redirects unless told otherwise, which would compare the
// expected status against whatever the redirect lands on.
func TestRenderHonoursFollowRedirects(t *testing.T) {
	for name, tc := range map[string]struct {
		annotated string
		want      bool
	}{
		"default": {annotated: "", want: true},
		"off":     {annotated: "false", want: false},
		"on":      {annotated: "true", want: true},
	} {
		t.Run(name, func(t *testing.T) {
			annotations := map[string]string{AnnotationHealthcheckPath: "/healthz"}
			if tc.annotated != "" {
				annotations[AnnotationHealthcheckFollowRedirects] = tc.annotated
			}

			bp, _ := Render(defaultInputs(route("demo", "web", withAnnotations(annotations))))

			hc := bp.PublicResources["gw-demo-web"].Targets[0].Healthcheck
			if hc == nil {
				t.Fatal("no healthcheck on the target")
			}
			if hc.FollowRedirects != tc.want {
				t.Errorf("follow-redirects = %t, want %t", hc.FollowRedirects, tc.want)
			}
		})
	}
}

func TestRenderRejectsAnUnreadableFollowRedirects(t *testing.T) {
	r := route("demo", "web", withAnnotations(map[string]string{
		AnnotationHealthcheckPath:            "/healthz",
		AnnotationHealthcheckFollowRedirects: "sometimes",
	}))

	bp, verdicts := Render(defaultInputs(r))

	if _, published := bp.PublicResources["gw-demo-web"]; published {
		t.Error("route with an unreadable follow-redirects was published")
	}
	if v := verdictFor(t, verdicts, "demo", "web"); v.Accepted.Status != metav1.ConditionFalse {
		t.Errorf("Accepted = %+v, want False", v.Accepted)
	}
}

// Every tuning annotation without a path is the same mistake: the author
// expected a check and would not get one.
func TestRenderRejectsTheNewHealthcheckSettingsWithoutAPath(t *testing.T) {
	for name, annotations := range map[string]map[string]string{
		"status only":           {AnnotationHealthcheckStatus: "400"},
		"method only":           {AnnotationHealthcheckMethod: "HEAD"},
		"follow-redirects only": {AnnotationHealthcheckFollowRedirects: "false"},
	} {
		t.Run(name, func(t *testing.T) {
			bp, verdicts := Render(defaultInputs(route("demo", "web", withAnnotations(annotations))))

			if _, published := bp.PublicResources["gw-demo-web"]; published {
				t.Error("route was published with healthcheck settings that do nothing")
			}
			v := verdictFor(t, verdicts, "demo", "web")
			if v.Accepted.Reason != string(gatewayv1.RouteReasonUnsupportedValue) {
				t.Errorf("reason = %q, want UnsupportedValue", v.Accepted.Reason)
			}
		})
	}
}

// A target with no 2xx path at all is better described by a check that only
// opens a connection than by one that insists on a status it cannot give.
func TestRenderPublishesATCPHealthcheck(t *testing.T) {
	r := route("demo", "web", withAnnotations(map[string]string{
		AnnotationHealthcheckMode: pangolin.HealthcheckModeTCP,
	}))

	bp, _ := Render(defaultInputs(r))

	target := bp.PublicResources["gw-demo-web"].Targets[0]
	hc := target.Healthcheck
	if hc == nil {
		t.Fatal("no healthcheck on the target")
	}
	if got, want := hc.Mode, pangolin.HealthcheckModeTCP; got != want {
		t.Errorf("mode = %q, want %q", got, want)
	}
	if hc.Hostname != target.Hostname || hc.Port != target.Port {
		t.Errorf("healthcheck addresses %s:%d, want the target's %s:%d",
			hc.Hostname, hc.Port, target.Hostname, target.Port)
	}
	if hc.Path != "" || hc.Method != "" || hc.Status != 0 {
		t.Errorf("TCP check carries HTTP settings: path=%q method=%q status=%d",
			hc.Path, hc.Method, hc.Status)
	}
	if got, want := hc.Interval, DefaultHealthcheckInterval; got != want {
		t.Errorf("interval = %d, want the default %d", got, want)
	}
}

func TestRenderTunesATCPHealthcheck(t *testing.T) {
	r := route("demo", "web", withAnnotations(map[string]string{
		AnnotationHealthcheckMode:     pangolin.HealthcheckModeTCP,
		AnnotationHealthcheckInterval: "10",
		AnnotationHealthcheckTimeout:  "2",
	}))

	bp, _ := Render(defaultInputs(r))

	hc := bp.PublicResources["gw-demo-web"].Targets[0].Healthcheck
	if hc == nil {
		t.Fatal("no healthcheck on the target")
	}
	if hc.Interval != 10 || hc.Timeout != 2 {
		t.Errorf("interval/timeout = %d/%d, want 10/2", hc.Interval, hc.Timeout)
	}
}

// A TCP check never reads a response, so anything describing one is a
// misunderstanding worth reporting rather than ignoring.
func TestRenderRejectsHTTPSettingsOnATCPHealthcheck(t *testing.T) {
	for name, tc := range map[string]struct {
		annotation, value string
	}{
		"path":             {AnnotationHealthcheckPath, "/healthz"},
		"status":           {AnnotationHealthcheckStatus, "400"},
		"method":           {AnnotationHealthcheckMethod, "HEAD"},
		"follow-redirects": {AnnotationHealthcheckFollowRedirects, "false"},
	} {
		t.Run(name, func(t *testing.T) {
			r := route("demo", "web", withAnnotations(map[string]string{
				AnnotationHealthcheckMode: pangolin.HealthcheckModeTCP,
				tc.annotation:             tc.value,
			}))

			bp, verdicts := Render(defaultInputs(r))

			if _, published := bp.PublicResources["gw-demo-web"]; published {
				t.Error("route with an HTTP setting on a TCP check was published")
			}
			v := verdictFor(t, verdicts, "demo", "web")
			if !strings.Contains(v.Accepted.Message, tc.annotation) {
				t.Errorf("message %q does not name %q", v.Accepted.Message, tc.annotation)
			}
		})
	}
}

// Pangolin takes any string as the mode and falls back to HTTP for one it does
// not know, so a typo would quietly check something else entirely.
func TestRenderRejectsAnUnknownHealthcheckMode(t *testing.T) {
	for name, value := range map[string]string{
		"typo":       "tpc",
		"wrong case": "TCP",
		"empty":      "",
		"snmp":       "snmp",
		"icmp":       "icmp",
	} {
		t.Run(name, func(t *testing.T) {
			r := route("demo", "web", withAnnotations(map[string]string{
				AnnotationHealthcheckMode: value,
			}))

			bp, verdicts := Render(defaultInputs(r))

			if _, published := bp.PublicResources["gw-demo-web"]; published {
				t.Error("route with an unknown healthcheck mode was published")
			}
			if v := verdictFor(t, verdicts, "demo", "web"); v.Accepted.Status != metav1.ConditionFalse {
				t.Errorf("Accepted = %+v, want False", v.Accepted)
			}
		})
	}
}

// The mode names the strategy; it does not say what to request. An HTTP check
// still needs its path, and saying so beats publishing a route without a check.
func TestRenderRejectsHTTPModeWithoutAPath(t *testing.T) {
	r := route("demo", "web", withAnnotations(map[string]string{
		AnnotationHealthcheckMode: pangolin.HealthcheckModeHTTP,
	}))

	bp, verdicts := Render(defaultInputs(r))

	if _, published := bp.PublicResources["gw-demo-web"]; published {
		t.Error("route asking for an HTTP check without a path was published")
	}
	v := verdictFor(t, verdicts, "demo", "web")
	if !strings.Contains(v.Accepted.Message, AnnotationHealthcheckPath) {
		t.Errorf("message %q does not name %q", v.Accepted.Message, AnnotationHealthcheckPath)
	}
}

// An annotated path with no mode is the original spelling and still means HTTP.
func TestRenderDefaultsAPathOnlyHealthcheckToHTTPMode(t *testing.T) {
	r := route("demo", "web", withAnnotations(map[string]string{
		AnnotationHealthcheckPath: "/healthz",
	}))

	bp, _ := Render(defaultInputs(r))

	hc := bp.PublicResources["gw-demo-web"].Targets[0].Healthcheck
	if hc == nil {
		t.Fatal("no healthcheck on the target")
	}
	if got, want := hc.Mode, pangolin.HealthcheckModeHTTP; got != want {
		t.Errorf("mode = %q, want %q", got, want)
	}
}
