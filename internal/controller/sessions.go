// SPDX-License-Identifier: AGPL-3.0-only

package controller

import (
	"encoding/json"
	"sync"

	"github.com/p3l1/pangolin-gateway/internal/pangolin"
)

// appliedPins holds, per niceId, the JSON last applied for every entry
// carrying a PIN. Rationale: docs/sessions.md.
type appliedPins struct {
	mu        sync.Mutex
	resources map[string]string
	policies  map[string]string
}

// unchangedOmitted returns bp without the PIN-carrying entries whose JSON
// matches the last successful apply.
func (a *appliedPins) unchangedOmitted(bp pangolin.Blueprint) pangolin.Blueprint {
	a.mu.Lock()
	defer a.mu.Unlock()

	out := bp
	out.PublicResources = omitUnchanged(bp.PublicResources, a.resources, resourceHasPin)
	out.PublicPolicies = omitUnchanged(bp.PublicPolicies, a.policies, policyHasPin)
	return out
}

// remember records the PIN-carrying entries of a blueprint that was applied
// in full effect — sent, or skipped because already in place.
func (a *appliedPins) remember(desired pangolin.Blueprint) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.resources = fingerprints(desired.PublicResources, resourceHasPin)
	a.policies = fingerprints(desired.PublicPolicies, policyHasPin)
}

// retain forgets every remembered niceId missing from a complete listing, so
// an entry deleted in Pangolin is sent again on the next pass.
func (a *appliedPins) retain(visibility string, live []pangolin.Resource) {
	a.mu.Lock()
	defer a.mu.Unlock()

	var held map[string]string
	switch visibility {
	case visibilityPublic:
		held = a.resources
	case visibilityPolicy:
		held = a.policies
	default:
		return
	}
	present := make(map[string]bool, len(live))
	for _, r := range live {
		present[r.NiceID] = true
	}
	for k := range held {
		if !present[k] {
			delete(held, k)
		}
	}
}

func resourceHasPin(r pangolin.PublicResource) bool {
	return r.Auth != nil && r.Auth.Pincode != nil
}

func policyHasPin(p pangolin.PublicPolicy) bool {
	return p.Pincode != ""
}

func omitUnchanged[T any](entries map[string]T, held map[string]string, hasPin func(T) bool) map[string]T {
	if entries == nil {
		return nil
	}
	out := make(map[string]T, len(entries))
	for k, v := range entries {
		if hasPin(v) && held[k] != "" && held[k] == fingerprint(v) {
			continue
		}
		out[k] = v
	}
	return out
}

func fingerprints[T any](entries map[string]T, hasPin func(T) bool) map[string]string {
	out := map[string]string{}
	for k, v := range entries {
		if hasPin(v) {
			out[k] = fingerprint(v)
		}
	}
	return out
}

// fingerprint is empty when v does not marshal, which never matches.
func fingerprint(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(raw)
}
