// SPDX-License-Identifier: AGPL-3.0-only

package pangolin

import (
	"context"
	"sort"

	"github.com/go-logr/logr"
)

// DryRun wraps a Client so every write is logged instead of performed. It is a
// decorator rather than a conditional at each call site so no write can route
// around the flag by accident.
type DryRun struct {
	Inner Client
	Log   logr.Logger
}

var _ Client = (*DryRun)(nil)

func NewDryRun(inner Client, log logr.Logger) *DryRun {
	return &DryRun{Inner: inner, Log: log}
}

func (d *DryRun) ApplyBlueprint(_ context.Context, bp Blueprint) error {
	public := keysOf(bp.PublicResources)
	private := keysOf(bp.PrivateResources)

	d.Log.Info("dry run: would apply blueprint",
		"publicResources", len(public), "publicKeys", public,
		"privateResources", len(private), "privateKeys", private)
	return nil
}

func keysOf[T any](m map[string]T) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Reads pass through: the dry run must still show which resources the prune
// would have deleted, which needs the real listing.
func (d *DryRun) ListPublicResources(ctx context.Context) ([]Resource, error) {
	return d.Inner.ListPublicResources(ctx)
}

func (d *DryRun) ListPrivateResources(ctx context.Context) ([]Resource, error) {
	return d.Inner.ListPrivateResources(ctx)
}

func (d *DryRun) ListSites(ctx context.Context) ([]Site, error) {
	return d.Inner.ListSites(ctx)
}

func (d *DryRun) DeletePublicResource(_ context.Context, id int) error {
	d.Log.Info("dry run: would delete public resource", "resourceId", id)
	return nil
}

func (d *DryRun) DeletePrivateResource(_ context.Context, id int) error {
	d.Log.Info("dry run: would delete private resource", "siteResourceId", id)
	return nil
}
