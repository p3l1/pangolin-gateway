// SPDX-License-Identifier: AGPL-3.0-only

package pangolinfake_test

import (
	"context"
	"strings"
	"testing"

	"github.com/p3l1/pangolin-gateway/internal/pangolin"
)

func withMaintenance(m *pangolin.Maintenance) pangolin.Blueprint {
	res := resource("a.example.com")
	res.Maintenance = m
	return pangolin.Blueprint{PublicResources: map[string]pangolin.PublicResource{"gw-demo-a": res}}
}

func TestFakeKeepsTheMaintenancePageAnApplyLeavesOut(t *testing.T) {
	f, c := newFake(t)
	f.SetLicensed(true)
	ctx := context.Background()

	page := &pangolin.Maintenance{Enabled: true, Type: "forced", Title: "Pause", Message: "Bitte warten."}
	if err := c.ApplyBlueprint(ctx, withMaintenance(page)); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	if err := c.ApplyBlueprint(ctx, withMaintenance(nil)); err != nil {
		t.Fatalf("second apply: %v", err)
	}
	if got := f.Resources()["gw-demo-a"].Maintenance; got == nil || !got.Enabled {
		t.Fatalf("maintenance = %+v, want the stored page kept", got)
	}

	if err := c.ApplyBlueprint(ctx, withMaintenance(&pangolin.Maintenance{})); err != nil {
		t.Fatalf("third apply: %v", err)
	}
	got := f.Resources()["gw-demo-a"].Maintenance
	if got == nil || got.Enabled || got.Title != "Pause" {
		t.Errorf("maintenance = %+v, want disabled with the stored text kept", got)
	}
}

func TestFakeDropsTheMaintenancePageUnlicensed(t *testing.T) {
	f, c := newFake(t)
	page := &pangolin.Maintenance{Enabled: true, Type: "forced", Title: "Pause", Message: "Bitte warten."}
	if err := c.ApplyBlueprint(context.Background(), withMaintenance(page)); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got := f.Resources()["gw-demo-a"].Maintenance; got != nil {
		t.Errorf("maintenance = %+v, want it dropped", got)
	}
}

func TestFakeRejectsAMaintenancePageOutsideTheSchema(t *testing.T) {
	for name, m := range map[string]*pangolin.Maintenance{
		"type":  {Enabled: true, Type: "sometimes"},
		"title": {Enabled: true, Type: "forced", Title: strings.Repeat("a", 256)},
	} {
		t.Run(name, func(t *testing.T) {
			f, c := newFake(t)
			f.SetLicensed(true)
			if err := c.ApplyBlueprint(context.Background(), withMaintenance(m)); err == nil {
				t.Error("apply succeeded; Pangolin rejects the whole document")
			}
		})
	}
}
