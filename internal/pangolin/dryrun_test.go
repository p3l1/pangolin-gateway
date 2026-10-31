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

func (c *recordingClient) ListResourcePolicies(context.Context) ([]Resource, error) {
	return []Resource{{ResourceID: 3, NiceID: "gw-demo-choir"}}, nil
}

func (c *recordingClient) DeleteResourcePolicy(context.Context, int) error {
	c.t.Error("DeleteResourcePolicy reached the inner client during a dry run")
	return nil
}

func (c *recordingClient) ListSites(context.Context) ([]Site, error) {
	return []Site{{NiceID: "test-site"}}, nil
}

func (c *recordingClient) ListMachines(context.Context) ([]Machine, error) {
	return []Machine{{NiceID: "ci-runner"}}, nil
}

func (c *recordingClient) ListUsers(context.Context) ([]User, error) {
	return []User{{Username: "alice", Email: "alice@example.com"}}, nil
}

func (c *recordingClient) ListResourceMtlsCertificates(context.Context, int) ([]MtlsCertificate, error) {
	c.t.Error("ListResourceMtlsCertificates reached the inner client during a dry run")
	return nil, nil
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
	if err := d.DeleteResourcePolicy(ctx, 3); err != nil {
		t.Errorf("DeleteResourcePolicy returned %v, want nil", err)
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

// The machine and user checks run in a dry run too, so both listings must reach
// the real instance.
func TestDryRunStillListsGrantees(t *testing.T) {
	d := NewDryRun(&recordingClient{t: t}, logr.Discard())

	machines, err := d.ListMachines(context.Background())
	if err != nil || len(machines) != 1 || machines[0].NiceID != "ci-runner" {
		t.Errorf("ListMachines = %+v, %v; want the inner client's row", machines, err)
	}
	users, err := d.ListUsers(context.Background())
	if err != nil || len(users) != 1 || users[0].Username != "alice" {
		t.Errorf("ListUsers = %+v, %v; want the inner client's row", users, err)
	}
}

func TestDryRunStillListsPolicies(t *testing.T) {
	d := NewDryRun(&recordingClient{t: t}, logr.Discard())

	got, err := d.ListResourcePolicies(context.Background())
	if err != nil || len(got) != 1 || got[0].NiceID != "gw-demo-choir" {
		t.Errorf("ListResourcePolicies = %+v, %v; want the inner client's row", got, err)
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

// mTLS verification does not run in a rehearsal, so the decorator answers empty
// without reaching the instance; the inner client would t.Error if it were hit.
func TestDryRunSkipsMtlsVerification(t *testing.T) {
	d := NewDryRun(&recordingClient{t: t}, logr.Discard())

	got, err := d.ListResourceMtlsCertificates(context.Background(), 7)
	if err != nil {
		t.Errorf("ListResourceMtlsCertificates returned %v, want nil", err)
	}
	if len(got) != 0 {
		t.Errorf("ListResourceMtlsCertificates = %+v, want empty", got)
	}
}
