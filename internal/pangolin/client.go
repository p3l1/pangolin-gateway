// SPDX-License-Identifier: AGPL-3.0-only

package pangolin

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Client is the whole of Pangolin this controller needs. Keeping it this narrow
// means a correction to the API shape lands in one implementation.
type Client interface {
	ApplyBlueprint(ctx context.Context, bp Blueprint) error
	ListPublicResources(ctx context.Context) ([]Resource, error)
	ListSites(ctx context.Context) ([]Site, error)
	DeletePublicResource(ctx context.Context, id int) error
}

// The API key action each route is guarded by, named in AuthError so a denial
// points at the permission to add rather than at a generic 401.
const (
	actionApplyBlueprint = "applyBlueprint"
	actionListResources  = "listResources"
	actionListSites      = "listSites"
	actionDeleteResource = "deleteResource"
)

const (
	listPageSize   = 100
	defaultTimeout = 30 * time.Second
)

type Options struct {
	// Endpoint is the Integration API base URL including its version path, e.g.
	// https://api.example.com/v1. Cloud serves /v1 and self-hosted /api/v1, so a
	// client appending the segment itself guesses wrong exactly where the error
	// comes back as an opaque 401.
	Endpoint string
	OrgID    string
	APIKey   string
	Timeout  time.Duration
}

type HTTPClient struct {
	endpoint string
	orgID    string
	apiKey   string
	http     *http.Client
}

var _ Client = (*HTTPClient)(nil)

func NewHTTPClient(o Options) (*HTTPClient, error) {
	switch {
	case o.Endpoint == "":
		return nil, fmt.Errorf("pangolin endpoint is empty")
	case o.OrgID == "":
		return nil, fmt.Errorf("pangolin organisation id is empty")
	case o.APIKey == "":
		return nil, fmt.Errorf("pangolin API key is empty")
	}
	// An integration key is "<key_id>.<key_secret>"; a key pasted without the id
	// half authenticates against nothing and fails as a bare 401 much later.
	if !strings.Contains(o.APIKey, ".") {
		return nil, fmt.Errorf("pangolin API key is not of the form <key_id>.<key_secret>")
	}
	u, err := url.Parse(o.Endpoint)
	if err != nil {
		return nil, fmt.Errorf("parsing pangolin endpoint %q: %w", o.Endpoint, err)
	}
	if u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("pangolin endpoint %q needs a scheme and a host", o.Endpoint)
	}

	timeout := o.Timeout
	if timeout == 0 {
		timeout = defaultTimeout
	}
	return &HTTPClient{
		endpoint: strings.TrimSuffix(o.Endpoint, "/"),
		orgID:    o.OrgID,
		apiKey:   o.APIKey,
		http:     &http.Client{Timeout: timeout},
	}, nil
}

// AuthError marks a 401 or 403. It is a distinct type because the three causes —
// a key missing an action, an endpoint pointing at the internal API, a malformed
// key — are indistinguishable from the status code alone.
type AuthError struct {
	Status int
	Action string
	Body   string
}

func (e *AuthError) Error() string {
	return fmt.Sprintf(
		"pangolin returned %d: the API key may lack the %q action, "+
			"or the endpoint may not be the Integration API (body: %s)",
		e.Status, e.Action, truncate(e.Body, 200))
}

type apiError struct {
	Status int
	Body   string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("pangolin returned %d (body: %s)", e.Status, truncate(e.Body, 200))
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// envelope is the response shape every Integration API route returns.
type envelope[T any] struct {
	Data    T      `json:"data"`
	Success bool   `json:"success"`
	Error   bool   `json:"error"`
	Message string `json:"message"`
	Status  int    `json:"status"`
}

type sitesData struct {
	Sites      []Site `json:"sites"`
	Pagination struct {
		Total    int `json:"total"`
		PageSize int `json:"pageSize"`
		Page     int `json:"page"`
	} `json:"pagination"`
}

type listData struct {
	Resources  []Resource `json:"resources"`
	Pagination struct {
		Total    int `json:"total"`
		PageSize int `json:"pageSize"`
		Page     int `json:"page"`
	} `json:"pagination"`
}

func (c *HTTPClient) do(ctx context.Context, method, path, action string, body any) ([]byte, error) {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("encoding request body: %w", err)
		}
		reader = bytes.NewReader(raw)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.endpoint+path, reader)
	if err != nil {
		return nil, fmt.Errorf("building %s %s: %w", method, path, err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("reading %s %s response: %w", method, path, err)
	}

	switch {
	case resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusForbidden:
		return nil, &AuthError{Status: resp.StatusCode, Action: action, Body: string(raw)}
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		return nil, &apiError{Status: resp.StatusCode, Body: string(raw)}
	}
	return raw, nil
}

func (c *HTTPClient) ApplyBlueprint(ctx context.Context, bp Blueprint) error {
	raw, err := json.Marshal(bp)
	if err != nil {
		return fmt.Errorf("encoding blueprint: %w", err)
	}

	body := struct {
		Blueprint string `json:"blueprint"`
	}{Blueprint: base64.StdEncoding.EncodeToString(raw)}

	path := "/org/" + url.PathEscape(c.orgID) + "/blueprint"
	if _, err := c.do(ctx, http.MethodPut, path, actionApplyBlueprint, body); err != nil {
		return fmt.Errorf("applying blueprint: %w", err)
	}
	return nil
}

func (c *HTTPClient) ListPublicResources(ctx context.Context) ([]Resource, error) {
	var all []Resource

	for page := 1; ; page++ {
		path := fmt.Sprintf("/org/%s/public-resources?page=%s&pageSize=%s",
			url.PathEscape(c.orgID), strconv.Itoa(page), strconv.Itoa(listPageSize))

		raw, err := c.do(ctx, http.MethodGet, path, actionListResources, nil)
		if err != nil {
			return nil, fmt.Errorf("listing public resources (page %d): %w", page, err)
		}

		var env envelope[listData]
		if err := json.Unmarshal(raw, &env); err != nil {
			return nil, fmt.Errorf("decoding public resources (page %d): %w", page, err)
		}

		// An instance that ignores the page parameter would serve page one forever;
		// the prune must fail loudly rather than spin, since it deletes on the result.
		if got := env.Data.Pagination.Page; got != 0 && got != page {
			return nil, fmt.Errorf(
				"listing public resources stalled: asked for page %d, got page %d", page, got)
		}

		all = append(all, env.Data.Resources...)
		if len(env.Data.Resources) == 0 || len(all) >= env.Data.Pagination.Total {
			return all, nil
		}
	}
}

// ListSites reports the sites a blueprint target may name. Pangolin rejects the
// whole apply for one unknown site, so the renderer checks names against this
// rather than letting one mistyped annotation unpublish everything.
func (c *HTTPClient) ListSites(ctx context.Context) ([]Site, error) {
	var all []Site

	for page := 1; ; page++ {
		path := fmt.Sprintf("/org/%s/sites?page=%s&pageSize=%s",
			url.PathEscape(c.orgID), strconv.Itoa(page), strconv.Itoa(listPageSize))

		raw, err := c.do(ctx, http.MethodGet, path, actionListSites, nil)
		if err != nil {
			return nil, fmt.Errorf("listing sites (page %d): %w", page, err)
		}

		var env envelope[sitesData]
		if err := json.Unmarshal(raw, &env); err != nil {
			return nil, fmt.Errorf("decoding sites (page %d): %w", page, err)
		}
		if got := env.Data.Pagination.Page; got != 0 && got != page {
			return nil, fmt.Errorf("listing sites stalled: asked for page %d, got page %d", page, got)
		}

		all = append(all, env.Data.Sites...)
		if len(env.Data.Sites) == 0 || len(all) >= env.Data.Pagination.Total {
			return all, nil
		}
	}
}

func (c *HTTPClient) DeletePublicResource(ctx context.Context, id int) error {
	path := "/public-resource/" + strconv.Itoa(id)
	if _, err := c.do(ctx, http.MethodDelete, path, actionDeleteResource, nil); err != nil {
		return fmt.Errorf("deleting public resource %d: %w", id, err)
	}
	return nil
}
