// SPDX-License-Identifier: AGPL-3.0-only

package pangolinfake_test

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/p3l1/pangolin-gateway/internal/pangolin"
	"github.com/p3l1/pangolin-gateway/test/pangolinfake"
)

func newFake(t *testing.T) (*pangolinfake.Fake, pangolin.Client) {
	t.Helper()

	f := pangolinfake.New()
	srv := httptest.NewServer(f.Handler())
	t.Cleanup(srv.Close)

	c, err := pangolin.NewHTTPClient(pangolin.Options{
		Endpoint: srv.URL + "/v1",
		OrgID:    "test-org",
		APIKey:   "id.secret",
	})
	if err != nil {
		t.Fatalf("building client: %v", err)
	}
	return f, c
}

func resource(host string) pangolin.PublicResource {
	return pangolin.PublicResource{
		Name:       "demo/web",
		Mode:       pangolin.ModeHTTP,
		FullDomain: host,
		Auth:       &pangolin.Auth{SSOEnabled: true},
	}
}

// The fake must be additive without prune, because that gap is the reason this
// controller exists; a fake that prunes would hide every prune bug.
func TestFakeKeepsResourcesAbsentFromALaterApply(t *testing.T) {
	f, c := newFake(t)
	ctx := context.Background()

	first := pangolin.Blueprint{PublicResources: map[string]pangolin.PublicResource{
		"gw-demo-a": resource("a.example.com"),
		"gw-demo-b": resource("b.example.com"),
	}}
	if err := c.ApplyBlueprint(ctx, first); err != nil {
		t.Fatalf("first apply: %v", err)
	}

	second := pangolin.Blueprint{PublicResources: map[string]pangolin.PublicResource{
		"gw-demo-a": resource("a.example.com"),
	}}
	if err := c.ApplyBlueprint(ctx, second); err != nil {
		t.Fatalf("second apply: %v", err)
	}

	got := f.Resources()
	if _, ok := got["gw-demo-b"]; !ok {
		t.Error("the fake pruned gw-demo-b on its own; it must stay until deleted")
	}
	if len(got) != 2 {
		t.Errorf("resources = %v, want both to survive", got)
	}
}

func TestFakeListsSeededResourcesWithStableIDs(t *testing.T) {
	f, c := newFake(t)
	id := f.Seed("gw-demo-orphan", resource("orphan.example.com"))

	rows, err := c.ListPublicResources(context.Background())
	if err != nil {
		t.Fatalf("listing: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %+v, want one", rows)
	}
	if rows[0].NiceID != "gw-demo-orphan" || rows[0].ResourceID != id {
		t.Errorf("row = %+v, want niceId gw-demo-orphan with id %d", rows[0], id)
	}
}

func TestFakePaginates(t *testing.T) {
	f, c := newFake(t)
	for _, n := range []string{"gw-a", "gw-b", "gw-c", "gw-d", "gw-e"} {
		f.Seed(n, resource(n+".example.com"))
	}

	rows, err := c.ListPublicResources(context.Background())
	if err != nil {
		t.Fatalf("listing: %v", err)
	}
	if len(rows) != 5 {
		t.Errorf("got %d rows across pages, want 5: %+v", len(rows), rows)
	}
}

func TestFakeDeleteRemovesTheResource(t *testing.T) {
	f, c := newFake(t)
	id := f.Seed("gw-demo-orphan", resource("orphan.example.com"))

	if err := c.DeletePublicResource(context.Background(), id); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if got := f.Resources(); len(got) != 0 {
		t.Errorf("resources = %v, want empty after delete", got)
	}
	if got := f.Deleted(); len(got) != 1 || got[0] != id {
		t.Errorf("deleted = %v, want [%d]", got, id)
	}
}

func TestFakeInjectsFailures(t *testing.T) {
	f, c := newFake(t)
	ctx := context.Background()
	id := f.Seed("gw-demo-orphan", resource("orphan.example.com"))

	f.FailApply(http.StatusInternalServerError)
	if err := c.ApplyBlueprint(ctx, pangolin.Blueprint{}); err == nil {
		t.Error("apply succeeded despite an injected 500")
	}
	f.FailApply(0)

	// The CrowdSec/HTTP-3 failure mode from pangolin#3515 surfaces exactly here.
	f.FailDelete(http.StatusForbidden)
	if err := c.DeletePublicResource(ctx, id); err == nil {
		t.Error("delete succeeded despite an injected 403")
	}
	f.FailDelete(0)

	f.FailList(http.StatusUnauthorized)
	_, err := c.ListPublicResources(ctx)
	var authErr *pangolin.AuthError
	if !errors.As(err, &authErr) {
		t.Errorf("listing error = %v, want an *AuthError", err)
	}
}

func TestFakeRejectsDuplicateFullDomains(t *testing.T) {
	_, c := newFake(t)

	err := c.ApplyBlueprint(context.Background(), pangolin.Blueprint{
		PublicResources: map[string]pangolin.PublicResource{
			"gw-demo-a": resource("shared.example.com"),
			"gw-demo-b": resource("shared.example.com"),
		},
	})
	if err == nil {
		t.Fatal("apply accepted two resources claiming one full-domain")
	}
}

func TestFakeRejectsAMalformedKey(t *testing.T) {
	f := pangolinfake.New()
	srv := httptest.NewServer(f.Handler())
	t.Cleanup(srv.Close)

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/org/o/public-resources", nil)
	req.Header.Set("Authorization", "Bearer nodot")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 for a key without a dot", resp.StatusCode)
	}
}

// One bad value rejects the whole document, which is the behaviour the
// renderer's own validation exists to stay ahead of.
func TestFakeRejectsTheWholeApplyForOneInvalidRule(t *testing.T) {
	for name, rules := range map[string]pangolin.Rules{
		"bad ip":       {{Action: pangolin.ActionDeny, Match: pangolin.MatchIP, Value: "nope"}},
		"bad cidr":     {{Action: pangolin.ActionDeny, Match: pangolin.MatchCIDR, Value: "203.0.113.0"}},
		"bad path":     {{Action: pangolin.ActionAllow, Match: pangolin.MatchPath, Value: "/a//b"}},
		"bad action":   {{Action: "block", Match: pangolin.MatchPath, Value: "/x"}},
		"bad match":    {{Action: pangolin.ActionAllow, Match: "header", Value: "/x"}},
		"empty action": {{Match: pangolin.MatchPath, Value: "/x"}},
	} {
		t.Run(name, func(t *testing.T) {
			f, c := newFake(t)

			good := resource("good.example.com")
			bad := resource("bad.example.com")
			bad.Rules = rules

			err := c.ApplyBlueprint(context.Background(), pangolin.Blueprint{
				PublicResources: map[string]pangolin.PublicResource{
					"gw-demo-good": good,
					"gw-demo-bad":  bad,
				},
			})
			if err == nil {
				t.Fatal("the apply succeeded, want it rejected")
			}
			// All or nothing: the valid resource must not have been created either.
			if got := f.Resources(); len(got) != 0 {
				t.Errorf("fake holds %v, want nothing applied", got)
			}
		})
	}
}

func TestFakeAcceptsWellFormedRules(t *testing.T) {
	f, c := newFake(t)

	r := resource("web.example.com")
	r.Rules = pangolin.Rules{
		{Action: pangolin.ActionAllow, Match: pangolin.MatchPath, Value: "/script.js"},
		{Action: pangolin.ActionPass, Match: pangolin.MatchCIDR, Value: "203.0.113.0/24"},
		{Action: pangolin.ActionDeny, Match: pangolin.MatchCountry, Value: "ALL"},
	}

	if err := c.ApplyBlueprint(context.Background(), pangolin.Blueprint{
		PublicResources: map[string]pangolin.PublicResource{"gw-demo-web": r},
	}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got := f.Resources()["gw-demo-web"].Rules; len(got) != 3 {
		t.Errorf("rules = %+v, want all three stored", got)
	}
}

// Pangolin's schema takes an array or no key at all. A nil slice encoded the
// usual way would send null and fail the apply, which is why Rules marshals it
// as an empty array.
func TestFakeRejectsNullRules(t *testing.T) {
	f, _ := newFake(t)
	srv := httptest.NewServer(f.Handler())
	t.Cleanup(srv.Close)

	body := `{"blueprint":"` + base64.StdEncoding.EncodeToString([]byte(
		`{"public-resources":{"gw-demo-web":{"name":"demo/web","mode":"http","rules":null}}}`,
	)) + `"}`

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPut,
		srv.URL+"/v1/org/test-org/blueprint", strings.NewReader(body))
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer id.secret")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("applying: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for a null rules array", resp.StatusCode)
	}
}
