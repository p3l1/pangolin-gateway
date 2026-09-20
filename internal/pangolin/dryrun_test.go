// SPDX-License-Identifier: AGPL-3.0-only

package pangolin

import (
	"context"
	"errors"
	"testing"

	"github.com/go-logr/logr"
)

// recordingClient fails loudly on any write, so the decorator is tested by the
// absence of calls rather than by inspecting log output.
type recordingClient struct {
	t             *testing.T
	listed        int
	listedPrivate int
}

func (c *recordingClient) ApplyBlueprint(context.Context, Blueprint) error {
	c.t.Error("ApplyBlueprint reached the inner client during a dry run")
	return nil
}

func (c *recordingClient) DeletePublicResource(context.Context, int) error {
	c.t.Error("DeletePublicResource reached the inner client during a dry run")
	return nil
}

func (c *recordingClient) DeletePrivateResource(context.Context, int) error {
	c.t.Error("DeletePrivateResource reached the inner client during a dry run")
	return nil
}

func (c *recordingClient) ListPrivateResources(context.Context) ([]Resource, error) {
	c.listedPrivate++
	return []Resource{{ResourceID: 7, NiceID: "gw-demo-private"}}, nil
}

func (c *recordingClient) ListPublicResources(context.Context) ([]Resource, error) {
	c.listed++
	return []Resource{{ResourceID: 1, NiceID: "gw-demo-web"}}, nil
}

func (c *recordingClient) ListSites(context.Context) ([]Site, error) {
	return []Site{{NiceID: "test-site"}}, nil
}

func TestDryRunLetsNoWriteThrough(t *testing.T) {
	inner := &recordingClient{t: t}
	d := NewDryRun(inner, logr.Discard())
	ctx := context.Background()

	if err := d.ApplyBlueprint(ctx, Blueprint{PublicResources: map[string]PublicResource{
		"gw-demo-web": {Name: "demo/web", Mode: ModeHTTP},
	}}); err != nil {
		t.Errorf("ApplyBlueprint returned %v, want nil", err)
	}
	if err := d.DeletePublicResource(ctx, 42); err != nil {
		t.Errorf("DeletePublicResource returned %v, want nil", err)
	}
	if err := d.DeletePrivateResource(ctx, 7); err != nil {
		t.Errorf("DeletePrivateResource returned %v, want nil", err)
	}
}

// Both prunes must stay visible in a rehearsal, so both listings pass through.
func TestDryRunStillReadsBothSections(t *testing.T) {
	inner := &recordingClient{t: t}
	d := NewDryRun(inner, logr.Discard())
	ctx := context.Background()

	if _, err := d.ListPublicResources(ctx); err != nil {
		t.Fatalf("ListPublicResources: %v", err)
	}
	if _, err := d.ListPrivateResources(ctx); err != nil {
		t.Fatalf("ListPrivateResources: %v", err)
	}
	if inner.listed != 1 || inner.listedPrivate != 1 {
		t.Errorf("inner client saw %d public and %d private listings, want one each",
			inner.listed, inner.listedPrivate)
	}
}

// The prune's reasoning must stay visible in a dry run, which needs the listing.
func TestDryRunStillReads(t *testing.T) {
	inner := &recordingClient{t: t}
	d := NewDryRun(inner, logr.Discard())

	got, err := d.ListPublicResources(context.Background())
	if err != nil {
		t.Fatalf("ListPublicResources: %v", err)
	}
	if inner.listed != 1 {
		t.Errorf("inner client saw %d listings, want 1", inner.listed)
	}
	if len(got) != 1 || got[0].NiceID != "gw-demo-web" {
		t.Errorf("listing = %+v, want the inner client's rows", got)
	}
}

type failingLister struct{ recordingClient }

func (f *failingLister) ListPublicResources(context.Context) ([]Resource, error) {
	return nil, errors.New("boom")
}

func TestDryRunPropagatesReadErrors(t *testing.T) {
	d := NewDryRun(&failingLister{recordingClient{t: t}}, logr.Discard())

	if _, err := d.ListPublicResources(context.Background()); err == nil {
		t.Error("a failing listing was swallowed")
	}
}
