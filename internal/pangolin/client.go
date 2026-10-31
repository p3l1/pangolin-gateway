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
	ListPrivateResources(ctx context.Context) ([]Resource, error)
	ListSites(ctx context.Context) ([]Site, error)
	ListMachines(ctx context.Context) ([]Machine, error)
	ListUsers(ctx context.Context) ([]User, error)
	DeletePublicResource(ctx context.Context, id int) error
	DeletePrivateResource(ctx context.Context, id int) error

	// ListResourcePolicies and DeleteResourcePolicy serve the prune of shared
	// policies, a licensed feature. A policy row reuses Resource, ResourceID
	// holding its resourcePolicyId.
	ListResourcePolicies(ctx context.Context) ([]Resource, error)
	DeleteResourcePolicy(ctx context.Context, id int) error

	// ListResourceMtlsCertificates reads a resource's trusted CA set, so an apply
	// of auth.mtls can be confirmed to have taken effect.
	ListResourceMtlsCertificates(ctx context.Context, id int) ([]MtlsCertificate, error)
}

// The API key action each route is guarded by, named in AuthError so a denial
// points at the permission to add rather than at a generic 401.
const (
	actionApplyBlueprint = "applyBlueprint"
	actionListResources  = "listResources"
	actionListSites      = "listSites"
	actionListClients    = "listClients"
	actionListUsers      = "listUsers"
	actionDeleteResource = "deleteResource"

	// Private resources live in their own table behind their own routes, so
	// they are guarded by their own pair of actions.
	actionListSiteResources  = "listSiteResources"
	actionDeleteSiteResource = "deleteSiteResource"

	// Shared resource policies are licensed and guarded by their own pair.
	actionListResourcePolicies = "listResourcePolicies"
	actionDeleteResourcePolicy = "deleteResourcePolicy"

	// Reading a resource's trusted CA set to confirm an mTLS apply took effect.
	actionListResourceMtlsCertificates = "listResourceMtlsCertificates"
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

// privateListData is the same listing in a different spelling: its rows sit
// under siteResources and name the numeric id siteResourceId. The quirk is kept
// here so the prune sees one row type for both sections.
type privateListData struct {
	SiteResources []struct {
		SiteResourceID int    `json:"siteResourceId"`
		NiceID         string `json:"niceId"`
	} `json:"siteResources"`
	Pagination struct {
		Total    int `json:"total"`
		PageSize int `json:"pageSize"`
		Page     int `json:"page"`
	} `json:"pagination"`
}

type pagination struct {
	Total    int `json:"total"`
	PageSize int `json:"pageSize"`
	Page     int `json:"page"`
}

type machinesData struct {
	Clients    []Machine  `json:"clients"`
	Pagination pagination `json:"pagination"`
}

type policiesData struct {
	Policies []struct {
		ResourcePolicyID int    `json:"resourcePolicyId"`
		NiceID           string `json:"niceId"`
	} `json:"policies"`
	Pagination pagination `json:"pagination"`
}

type usersData struct {
	Users      []User     `json:"users"`
	Pagination pagination `json:"pagination"`
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

// ListPrivateResources reports the private resources the organisation holds.
// They are absent from the public listing, so the prune would never see them.
func (c *HTTPClient) ListPrivateResources(ctx context.Context) ([]Resource, error) {
	var all []Resource

	for page := 1; ; page++ {
		path := fmt.Sprintf("/org/%s/private-resources?page=%s&pageSize=%s",
			url.PathEscape(c.orgID), strconv.Itoa(page), strconv.Itoa(listPageSize))

		raw, err := c.do(ctx, http.MethodGet, path, actionListSiteResources, nil)
		if err != nil {
			return nil, fmt.Errorf("listing private resources (page %d): %w", page, err)
		}

		var env envelope[privateListData]
		if err := json.Unmarshal(raw, &env); err != nil {
			return nil, fmt.Errorf("decoding private resources (page %d): %w", page, err)
		}
		if got := env.Data.Pagination.Page; got != 0 && got != page {
			return nil, fmt.Errorf(
				"listing private resources stalled: asked for page %d, got page %d", page, got)
		}

		for _, row := range env.Data.SiteResources {
			all = append(all, Resource{ResourceID: row.SiteResourceID, NiceID: row.NiceID})
		}
		if len(env.Data.SiteResources) == 0 || len(all) >= env.Data.Pagination.Total {
			return all, nil
		}
	}
}

// ListMachines reports the organisation's machine clients in every state. A
// blueprint resolves machines by niceId regardless of state, so the listing
// asks for blocked and archived clients too, which it omits by default.
func (c *HTTPClient) ListMachines(ctx context.Context) ([]Machine, error) {
	return listPages(ctx, c, "machine clients", "clients", "&status=active,blocked,archived",
		actionListClients, func(d machinesData) ([]Machine, pagination) {
			return d.Clients, d.Pagination
		})
}

// ListUsers reports the organisation's users, which a grant names by username
// or by e-mail address.
func (c *HTTPClient) ListUsers(ctx context.Context) ([]User, error) {
	return listPages(ctx, c, "users", "users", "", actionListUsers,
		func(d usersData) ([]User, pagination) { return d.Users, d.Pagination })
}

// ListResourcePolicies reports the organisation's shared policies. Pangolin
// lists only the global ones, which is what the blueprint creates.
func (c *HTTPClient) ListResourcePolicies(ctx context.Context) ([]Resource, error) {
	return listPages(ctx, c, "resource policies", "resource-policies", "",
		actionListResourcePolicies,
		func(d policiesData) ([]Resource, pagination) {
			rows := make([]Resource, 0, len(d.Policies))
			for _, p := range d.Policies {
				rows = append(rows, Resource{ResourceID: p.ResourcePolicyID, NiceID: p.NiceID})
			}
			return rows, d.Pagination
		})
}

// listPages walks a paginated listing under /org/{orgId}/{resource}. It fails
// rather than spins on an instance that ignores the page parameter.
func listPages[D, T any](
	ctx context.Context,
	c *HTTPClient,
	what, resource, extraQuery, action string,
	rows func(D) ([]T, pagination),
) ([]T, error) {
	var all []T

	for page := 1; ; page++ {
		path := fmt.Sprintf("/org/%s/%s?page=%d&pageSize=%d%s",
			url.PathEscape(c.orgID), resource, page, listPageSize, extraQuery)

		raw, err := c.do(ctx, http.MethodGet, path, action, nil)
		if err != nil {
			return nil, fmt.Errorf("listing %s (page %d): %w", what, page, err)
		}

		var env envelope[D]
		if err := json.Unmarshal(raw, &env); err != nil {
			return nil, fmt.Errorf("decoding %s (page %d): %w", what, page, err)
		}
		items, p := rows(env.Data)
		if p.Page != 0 && p.Page != page {
			return nil, fmt.Errorf("listing %s stalled: asked for page %d, got page %d",
				what, page, p.Page)
		}

		// Rows without a total cannot tell a complete listing from a first page,
		// and a short listing would make every later name look unknown.
		if len(items) > 0 && p.Total == 0 {
			return nil, fmt.Errorf("listing %s returned rows but no pagination total", what)
		}

		all = append(all, items...)
		if len(items) == 0 || len(all) >= p.Total {
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

func (c *HTTPClient) DeletePrivateResource(ctx context.Context, id int) error {
	path := "/private-resource/" + strconv.Itoa(id)
	if _, err := c.do(ctx, http.MethodDelete, path, actionDeleteSiteResource, nil); err != nil {
		return fmt.Errorf("deleting private resource %d: %w", id, err)
	}
	return nil
}

// DeleteResourcePolicy fails while any resource still references the policy.
func (c *HTTPClient) DeleteResourcePolicy(ctx context.Context, id int) error {
	path := "/resource-policy/" + strconv.Itoa(id)
	if _, err := c.do(ctx, http.MethodDelete, path, actionDeleteResourcePolicy, nil); err != nil {
		return fmt.Errorf("deleting resource policy %d: %w", id, err)
	}
	return nil
}

// MtlsCertificate is one trusted client-certificate CA as Pangolin stores it.
// Fingerprint is the SHA-256 of the certificate, which the verify step compares
// against the certificates it sent.
type MtlsCertificate struct {
	Fingerprint string `json:"fingerprint"`
	Subject     string `json:"subject"`
	NotAfter    string `json:"notAfter"`
}

type mtlsCertificatesData struct {
	Certificates []MtlsCertificate `json:"certificates"`
}

func (c *HTTPClient) ListResourceMtlsCertificates(ctx context.Context, id int) ([]MtlsCertificate, error) {
	path := "/public-resource/" + strconv.Itoa(id) + "/mtls/certificates"
	raw, err := c.do(ctx, http.MethodGet, path, actionListResourceMtlsCertificates, nil)
	if err != nil {
		return nil, fmt.Errorf("listing mTLS certificates for resource %d: %w", id, err)
	}

	var env envelope[mtlsCertificatesData]
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("decoding mTLS certificates for resource %d: %w", id, err)
	}
	return env.Data.Certificates, nil
}
