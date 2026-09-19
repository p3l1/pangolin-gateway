// SPDX-License-Identifier: AGPL-3.0-only

package pangolin

import (
	"context"

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
	keys := make([]string, 0, len(bp.PublicResources))
	for k := range bp.PublicResources {
		keys = append(keys, k)
	}
	d.Log.Info("dry run: would apply blueprint", "resources", len(keys), "keys", keys)
	return nil
}

// Reads pass through: the dry run must still show which resources the prune
// would have deleted, which needs the real listing.
func (d *DryRun) ListPublicResources(ctx context.Context) ([]Resource, error) {
	return d.Inner.ListPublicResources(ctx)
}

func (d *DryRun) ListSites(ctx context.Context) ([]Site, error) {
	return d.Inner.ListSites(ctx)
}

func (d *DryRun) DeletePublicResource(_ context.Context, id int) error {
	d.Log.Info("dry run: would delete public resource", "resourceId", id)
	return nil
}
