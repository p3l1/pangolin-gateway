// SPDX-License-Identifier: AGPL-3.0-only

package pangolin

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func testClient(t *testing.T, h http.Handler) *HTTPClient {
	t.Helper()

	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	c, err := NewHTTPClient(Options{
		Endpoint: srv.URL + "/v1",
		OrgID:    "test-org",
		APIKey:   "key_id.key_secret",
	})
	if err != nil {
		t.Fatalf("building client: %v", err)
	}
	return c
}

func TestApplyBlueprintPutsBase64EncodedJSON(t *testing.T) {
	var (
		gotMethod, gotPath, gotAuth, gotType string
		gotBlueprint                         Blueprint
	)
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotType = r.Header.Get("Content-Type")

		var body struct {
			Blueprint string `json:"blueprint"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decoding request body: %v", err)
		}
		raw, err := base64.StdEncoding.DecodeString(body.Blueprint)
		if err != nil {
			t.Errorf("blueprint is not base64: %v", err)
		}
		if err := json.Unmarshal(raw, &gotBlueprint); err != nil {
			t.Errorf("blueprint is not JSON: %v", err)
		}
		_, _ = fmt.Fprint(w, `{"success":true,"error":false,"message":"ok","status":200,"data":{}}`)
	}))

	bp := Blueprint{PublicResources: map[string]PublicResource{
		"gw-demo-web": {Name: "demo/web", Mode: ModeHTTP, FullDomain: "demo.example.com"},
	}}
	if err := c.ApplyBlueprint(context.Background(), bp); err != nil {
		t.Fatalf("ApplyBlueprint: %v", err)
	}

	if gotMethod != http.MethodPut {
		t.Errorf("method = %s, want PUT", gotMethod)
	}
	if want := "/v1/org/test-org/blueprint"; gotPath != want {
		t.Errorf("path = %s, want %s", gotPath, want)
	}
	if want := "Bearer key_id.key_secret"; gotAuth != want {
		t.Errorf("authorization = %q, want %q", gotAuth, want)
	}
	if !strings.HasPrefix(gotType, "application/json") {
		t.Errorf("content-type = %q, want application/json", gotType)
	}
	if got := gotBlueprint.PublicResources["gw-demo-web"].FullDomain; got != "demo.example.com" {
		t.Errorf("round-tripped full-domain = %q, want demo.example.com", got)
	}
}

func TestListPublicResourcesFollowsPagination(t *testing.T) {
	pages := map[string]string{
		"1": `{"success":true,"error":false,"message":"ok","status":200,"data":{
			"resources":[{"resourceId":1,"niceId":"gw-a-one"},{"resourceId":2,"niceId":"other"}],
			"pagination":{"total":3,"pageSize":2,"page":1}}}`,
		"2": `{"success":true,"error":false,"message":"ok","status":200,"data":{
			"resources":[{"resourceId":3,"niceId":"gw-b-two"}],
			"pagination":{"total":3,"pageSize":2,"page":2}}}`,
	}
	var paths []string
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		page := r.URL.Query().Get("page")
		if page == "" {
			page = "1"
		}
		body, ok := pages[page]
		if !ok {
			t.Errorf("unexpected page %q", page)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = fmt.Fprint(w, body)
	}))

	got, err := c.ListPublicResources(context.Background())
	if err != nil {
		t.Fatalf("ListPublicResources: %v", err)
	}

	if len(got) != 3 {
		t.Fatalf("got %d resources, want 3 across both pages: %+v", len(got), got)
	}
	want := map[string]int{"gw-a-one": 1, "other": 2, "gw-b-two": 3}
	for _, r := range got {
		if want[r.NiceID] != r.ResourceID {
			t.Errorf("resource %q has id %d, want %d", r.NiceID, r.ResourceID, want[r.NiceID])
		}
	}
	for _, p := range paths {
		if want := "/v1/org/test-org/public-resources"; p != want {
			t.Errorf("listing path = %s, want %s", p, want)
		}
	}
}

// A listing that never advances would otherwise spin forever against an instance
// whose pagination behaves unexpectedly.
func TestListPublicResourcesStopsOnAStallingPage(t *testing.T) {
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"success":true,"error":false,"message":"ok","status":200,"data":{
			"resources":[{"resourceId":1,"niceId":"gw-a-one"}],
			"pagination":{"total":99,"pageSize":1,"page":1}}}`)
	}))

	got, err := c.ListPublicResources(context.Background())
	if err == nil {
		t.Fatalf("want an error when the listing stalls, got %d resources", len(got))
	}
}

func TestDeletePublicResourceUsesTheNumericID(t *testing.T) {
	var gotMethod, gotPath string
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		_, _ = fmt.Fprint(w, `{"success":true,"error":false,"message":"deleted","status":200,"data":{}}`)
	}))

	if err := c.DeletePublicResource(context.Background(), 42); err != nil {
		t.Fatalf("DeletePublicResource: %v", err)
	}

	if gotMethod != http.MethodDelete {
		t.Errorf("method = %s, want DELETE", gotMethod)
	}
	if want := "/v1/public-resource/42"; gotPath != want {
		t.Errorf("path = %s, want %s", gotPath, want)
	}
}

// 401 and 403 have three plausible causes that look identical in a bare status
// code, so the error must name them; an afternoon of debugging hangs on this.
func TestAuthErrorsExplainTheLikelyCause(t *testing.T) {
	for _, code := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(code)
				_, _ = fmt.Fprint(w, `{"success":false,"error":true,"message":"denied","status":`+
					fmt.Sprint(code)+`,"data":null}`)
			}))

			err := c.ApplyBlueprint(context.Background(), Blueprint{})
			if err == nil {
				t.Fatal("want an error")
			}
			var authErr *AuthError
			if !errors.As(err, &authErr) {
				t.Fatalf("error %v is not an *AuthError", err)
			}
			for _, want := range []string{"applyBlueprint", "Integration API"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not mention %q", err, want)
				}
			}
		})
	}
}

func TestServerErrorsAreNotAuthErrors(t *testing.T) {
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))

	err := c.ApplyBlueprint(context.Background(), Blueprint{})
	if err == nil {
		t.Fatal("want an error")
	}
	var authErr *AuthError
	if errors.As(err, &authErr) {
		t.Errorf("502 reported as an auth error: %v", err)
	}
}

func TestNewHTTPClientRejectsIncompleteOptions(t *testing.T) {
	for name, opts := range map[string]Options{
		"no endpoint": {OrgID: "o", APIKey: "a.b"},
		"no org":      {Endpoint: "https://api.example.com/v1", APIKey: "a.b"},
		"no key":      {Endpoint: "https://api.example.com/v1", OrgID: "o"},
		"bad key":     {Endpoint: "https://api.example.com/v1", OrgID: "o", APIKey: "nodot"},
		"bad URL":     {Endpoint: "://nope", OrgID: "o", APIKey: "a.b"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NewHTTPClient(opts); err == nil {
				t.Errorf("NewHTTPClient(%+v) succeeded, want an error", opts)
			}
		})
	}
}
