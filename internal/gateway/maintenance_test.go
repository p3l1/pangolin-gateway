// SPDX-License-Identifier: AGPL-3.0-only

package gateway

import (
	"reflect"
	"strings"
	"testing"

	"github.com/p3l1/pangolin-gateway/api/v1alpha1"
	"github.com/p3l1/pangolin-gateway/internal/pangolin"
)

func maintenanceInputs(licensed bool, kv map[string]string) Inputs {
	in := defaultInputs(route("demo", "web", withAnnotations(kv)))
	in.Licensed = licensed
	return in
}

func TestRenderSetsTheMaintenancePage(t *testing.T) {
	for _, mode := range []string{MaintenanceAutomatic, MaintenanceForced} {
		t.Run(mode, func(t *testing.T) {
			bp, _ := Render(maintenanceInputs(true, map[string]string{
				AnnotationMaintenanceMode:          mode,
				AnnotationMaintenanceTitle:         "  Kurze Pause ",
				AnnotationMaintenanceMessage:       "Bitte versuch es gleich noch einmal.",
				AnnotationMaintenanceEstimatedTime: "ca. 10 Minuten",
			}))

			res, ok := bp.PublicResources["gw-demo-web"]
			if !ok {
				t.Fatal("route was not published")
			}
			want := &pangolin.Maintenance{
				Enabled:       true,
				Type:          mode,
				Title:         "Kurze Pause",
				Message:       "Bitte versuch es gleich noch einmal.",
				EstimatedTime: "ca. 10 Minuten",
			}
			if !reflect.DeepEqual(res.Maintenance, want) {
				t.Errorf("maintenance = %+v, want %+v", res.Maintenance, want)
			}
		})
	}
}

// Pangolin keeps the columns of an omitted block, so switching the page off
// has to be said explicitly.
func TestRenderSwitchesTheMaintenancePageOff(t *testing.T) {
	for name, kv := range map[string]map[string]string{
		"unannotated": nil,
		"text without mode": {
			AnnotationMaintenanceTitle:   "Kurze Pause",
			AnnotationMaintenanceMessage: "Bitte warten.",
		},
		"empty mode": {AnnotationMaintenanceMode: " "},
	} {
		t.Run(name, func(t *testing.T) {
			bp, _ := Render(maintenanceInputs(true, kv))
			res, ok := bp.PublicResources["gw-demo-web"]
			if !ok {
				t.Fatal("route was not published")
			}
			if !reflect.DeepEqual(res.Maintenance, &pangolin.Maintenance{}) {
				t.Errorf("maintenance = %+v, want {enabled:false}", res.Maintenance)
			}
		})
	}
}

func TestRenderSendsNoMaintenanceBlockUnlicensed(t *testing.T) {
	bp, _ := Render(maintenanceInputs(false, nil))
	if m := bp.PublicResources["gw-demo-web"].Maintenance; m != nil {
		t.Errorf("maintenance = %+v, want none", m)
	}
}

func TestRenderRejectsAMaintenancePagePangolinWouldRefuse(t *testing.T) {
	valid := map[string]string{
		AnnotationMaintenanceMode:    MaintenanceAutomatic,
		AnnotationMaintenanceTitle:   "Kurze Pause",
		AnnotationMaintenanceMessage: "Bitte warten.",
	}
	with := func(k, v string) map[string]string {
		kv := map[string]string{}
		for a, b := range valid {
			kv[a] = b
		}
		if v == "<unset>" {
			delete(kv, k)
		} else {
			kv[k] = v
		}
		return kv
	}

	for name, tc := range map[string]struct {
		licensed bool
		kv       map[string]string
		mention  string
	}{
		"unknown mode":      {true, with(AnnotationMaintenanceMode, "sometimes"), AnnotationMaintenanceMode},
		"mode in caps":      {true, with(AnnotationMaintenanceMode, "Forced"), AnnotationMaintenanceMode},
		"no title":          {true, with(AnnotationMaintenanceTitle, "<unset>"), AnnotationMaintenanceTitle},
		"blank title":       {true, with(AnnotationMaintenanceTitle, "  "), AnnotationMaintenanceTitle},
		"no message":        {true, with(AnnotationMaintenanceMessage, "<unset>"), AnnotationMaintenanceMessage},
		"blank message":     {true, with(AnnotationMaintenanceMessage, "\n"), AnnotationMaintenanceMessage},
		"long title":        {true, with(AnnotationMaintenanceTitle, strings.Repeat("a", 256)), AnnotationMaintenanceTitle},
		"long message":      {true, with(AnnotationMaintenanceMessage, strings.Repeat("a", 2001)), AnnotationMaintenanceMessage},
		"long time":         {true, with(AnnotationMaintenanceEstimatedTime, strings.Repeat("a", 101)), AnnotationMaintenanceEstimatedTime},
		"unlicensed":        {false, valid, "--pangolin-licensed"},
		"unlicensed text":   {false, map[string]string{AnnotationMaintenanceTitle: "Kurze Pause"}, "--pangolin-licensed"},
		"long without mode": {true, map[string]string{AnnotationMaintenanceTitle: strings.Repeat("a", 256)}, AnnotationMaintenanceTitle},
	} {
		t.Run(name, func(t *testing.T) {
			bp, verdicts := Render(maintenanceInputs(tc.licensed, tc.kv))

			if _, published := bp.PublicResources["gw-demo-web"]; published {
				t.Fatal("route was published with a maintenance page Pangolin refuses")
			}
			v := verdictFor(t, verdicts, "demo", "web")
			if !strings.Contains(v.Accepted.Message, tc.mention) {
				t.Errorf("message %q does not mention %s", v.Accepted.Message, tc.mention)
			}
		})
	}
}

// zod counts characters, not bytes, so a title of umlauts fits at 255.
func TestRenderMeasuresMaintenanceTextInCharacters(t *testing.T) {
	title := strings.Repeat("ü", 255)
	bp, _ := Render(maintenanceInputs(true, map[string]string{
		AnnotationMaintenanceMode:    MaintenanceForced,
		AnnotationMaintenanceTitle:   title,
		AnnotationMaintenanceMessage: "Bitte warten.",
	}))
	res, ok := bp.PublicResources["gw-demo-web"]
	if !ok {
		t.Fatal("a 255-character title was rejected")
	}
	if res.Maintenance.Title != title {
		t.Error("title was altered")
	}
}

func TestRenderRefusesTheMaintenancePageOnAPrivateRoute(t *testing.T) {
	bp, verdicts := Render(maintenanceInputs(true, map[string]string{
		AnnotationVisibility:         VisibilityPrivate,
		AnnotationMaintenanceMode:    MaintenanceForced,
		AnnotationMaintenanceTitle:   "Kurze Pause",
		AnnotationMaintenanceMessage: "Bitte warten.",
	}))
	if _, published := bp.PrivateResources["gw-demo-web"]; published {
		t.Fatal("private route was published with a maintenance page it cannot show")
	}
	v := verdictFor(t, verdicts, "demo", "web")
	if !strings.Contains(v.Accepted.Message, AnnotationMaintenanceMode) {
		t.Errorf("message %q does not name the annotation", v.Accepted.Message)
	}
}

func TestRenderKeepsTheMaintenancePageBesideAPolicy(t *testing.T) {
	in := licensedInputs([]v1alpha1.ResourcePolicy{choirPolicy()}, policyRoute("choir", map[string]string{
		AnnotationMaintenanceMode:    MaintenanceAutomatic,
		AnnotationMaintenanceTitle:   "Kurze Pause",
		AnnotationMaintenanceMessage: "Bitte warten.",
	}))

	res := RenderAll(in)

	web, ok := res.Blueprint.PublicResources["gw-demo-web"]
	if !ok {
		t.Fatal("route was not published")
	}
	if web.Policy == "" || web.Maintenance == nil || !web.Maintenance.Enabled {
		t.Errorf("policy = %q, maintenance = %+v; want both", web.Policy, web.Maintenance)
	}
}
