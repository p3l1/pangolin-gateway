// SPDX-License-Identifier: AGPL-3.0-only

// Package pangolinfake reproduces the Integration API routes this controller
// uses, so envtest and e2e never reach a real Pangolin instance.
package pangolinfake

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/p3l1/pangolin-gateway/internal/pangolin"
)

// Fake holds the resources a real instance would hold, and records every
// blueprint it received so tests can assert on what the controller sent.
type Fake struct {
	mu sync.Mutex

	received  []pangolin.Blueprint
	resources map[string]pangolin.PublicResource
	ids       map[string]int
	nextID    int
	deleted   []int
	sites     []string
	machines  []string
	users     []pangolin.User

	// Private resources live in their own table behind their own routes, with
	// their own id sequence: Pangolin's siteResourceId is not a resourceId.
	privateResources map[string]pangolin.PrivateResource
	privateIDs       map[string]int
	nextPrivateID    int
	privateDeleted   []int

	// Trusted client-certificate CAs per resource niceId. mtlsStrip makes apply
	// drop auth.mtls, reproducing an open-source Pangolin build.
	mtlsCerts map[string][]pangolin.MtlsCertificate
	mtlsStrip bool

	// Shared resource policies, a licensed feature: while licensed is false the
	// policy routes answer 403 and an apply naming a policy is rejected.
	licensed         bool
	policies         map[string]pangolin.PublicPolicy
	policyIDs        map[string]int
	nextPolicyID     int
	policyDeleted    []int
	failPolicyList   int
	failPolicyDelete int

	failApply         int
	failList          int
	failPrivateList   int
	failSites         int
	failMachines      int
	failUsers         int
	failDelete        int
	failPrivateDelete int
}

func New() *Fake {
	return &Fake{
		resources:        map[string]pangolin.PublicResource{},
		ids:              map[string]int{},
		nextID:           1,
		privateResources: map[string]pangolin.PrivateResource{},
		privateIDs:       map[string]int{},
		nextPrivateID:    1,
		mtlsCerts:        map[string][]pangolin.MtlsCertificate{},
		policies:         map[string]pangolin.PublicPolicy{},
		policyIDs:        map[string]int{},
		nextPolicyID:     1,
		// A real organisation always has at least one site; tests that care name
		// their own via AddSite.
		sites:    []string{"test-site", "default-site"},
		machines: []string{"test-machine"},
		users:    []pangolin.User{{Username: "test-user", Email: "test-user@example.com"}},
	}
}

// FailApply and friends make the next and all subsequent calls of that kind
// answer with the given status; zero restores normal behaviour.
func (f *Fake) FailApply(status int)  { f.mu.Lock(); f.failApply = status; f.mu.Unlock() }
func (f *Fake) FailList(status int)   { f.mu.Lock(); f.failList = status; f.mu.Unlock() }
func (f *Fake) FailDelete(status int) { f.mu.Lock(); f.failDelete = status; f.mu.Unlock() }
func (f *Fake) FailSites(status int)  { f.mu.Lock(); f.failSites = status; f.mu.Unlock() }

func (f *Fake) FailPrivateList(status int) {
	f.mu.Lock()
	f.failPrivateList = status
	f.mu.Unlock()
}

func (f *Fake) FailPrivateDelete(status int) {
	f.mu.Lock()
	f.failPrivateDelete = status
	f.mu.Unlock()
}

func (f *Fake) FailMachines(status int) { f.mu.Lock(); f.failMachines = status; f.mu.Unlock() }
func (f *Fake) FailUsers(status int)    { f.mu.Lock(); f.failUsers = status; f.mu.Unlock() }

func (f *Fake) FailPolicyList(status int) {
	f.mu.Lock()
	f.failPolicyList = status
	f.mu.Unlock()
}

func (f *Fake) FailPolicyDelete(status int) {
	f.mu.Lock()
	f.failPolicyDelete = status
	f.mu.Unlock()
}

// SetLicensed makes the organisation hold a licence for shared policies.
func (f *Fake) SetLicensed(on bool) { f.mu.Lock(); f.licensed = on; f.mu.Unlock() }

// Policies returns the shared policies the instance currently holds.
func (f *Fake) Policies() map[string]pangolin.PublicPolicy {
	f.mu.Lock()
	defer f.mu.Unlock()

	out := make(map[string]pangolin.PublicPolicy, len(f.policies))
	for k, v := range f.policies {
		out[k] = v
	}
	return out
}

// PolicyDeleted returns the resourcePolicyIds deleted so far, in order.
func (f *Fake) PolicyDeleted() []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int(nil), f.policyDeleted...)
}

// SeedPolicy adds a policy as if an earlier apply had created it.
func (f *Fake) SeedPolicy(niceID string, p pangolin.PublicPolicy) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.putPolicy(niceID, p)
}

// putPolicy assumes the lock is held.
func (f *Fake) putPolicy(niceID string, p pangolin.PublicPolicy) int {
	f.policies[niceID] = p
	if id, ok := f.policyIDs[niceID]; ok {
		return id
	}
	id := f.nextPolicyID
	f.nextPolicyID++
	f.policyIDs[niceID] = id
	return id
}

// AddMachine makes a machine client niceId resolvable.
func (f *Fake) AddMachine(niceID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.machines = append(f.machines, niceID)
}

// AddUser adds an organisation member a grant can name by username or e-mail.
func (f *Fake) AddUser(username, email string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.users = append(f.users, pangolin.User{Username: username, Email: email})
}

// AddSite makes a site niceId resolvable, as creating one in Pangolin would.
func (f *Fake) AddSite(niceID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sites = append(f.sites, niceID)
}

// LastBlueprint returns the most recently applied blueprint.
func (f *Fake) LastBlueprint() (pangolin.Blueprint, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if len(f.received) == 0 {
		return pangolin.Blueprint{}, false
	}
	return f.received[len(f.received)-1], true
}

// Applies reports how many blueprint applies have been received, so a test can
// tell "published once" from "published on every pass".
func (f *Fake) Applies() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.received)
}

// Resources returns the public resources the instance currently holds, keyed by
// niceId.
func (f *Fake) Resources() map[string]pangolin.PublicResource {
	f.mu.Lock()
	defer f.mu.Unlock()

	out := make(map[string]pangolin.PublicResource, len(f.resources))
	for k, v := range f.resources {
		out[k] = v
	}
	return out
}

// PrivateResources returns the private resources the instance currently holds.
func (f *Fake) PrivateResources() map[string]pangolin.PrivateResource {
	f.mu.Lock()
	defer f.mu.Unlock()

	out := make(map[string]pangolin.PrivateResource, len(f.privateResources))
	for k, v := range f.privateResources {
		out[k] = v
	}
	return out
}

// PrivateDeleted returns the siteResourceIds deleted so far, in order.
func (f *Fake) PrivateDeleted() []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int(nil), f.privateDeleted...)
}

// SeedPrivate adds a private resource as if an earlier apply had created it.
func (f *Fake) SeedPrivate(niceID string, r pangolin.PrivateResource) int {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.privateResources[niceID] = r
	if id, ok := f.privateIDs[niceID]; ok {
		return id
	}
	id := f.nextPrivateID
	f.nextPrivateID++
	f.privateIDs[niceID] = id
	return id
}

// Deleted returns the resource ids deleted so far, in order.
func (f *Fake) Deleted() []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int(nil), f.deleted...)
}

// Seed adds a resource as if some earlier apply had created it. Used to set up
// the orphan a prune is supposed to remove.
func (f *Fake) Seed(niceID string, r pangolin.PublicResource) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.put(niceID, r)
}

// put assumes the lock is held.
func (f *Fake) put(niceID string, r pangolin.PublicResource) int {
	f.resources[niceID] = r
	if id, ok := f.ids[niceID]; ok {
		return id
	}
	id := f.nextID
	f.nextID++
	f.ids[niceID] = id
	return id
}

// SetMtlsStrip makes apply drop auth.mtls, so tests can exercise the open-source
// build where the field is silently stripped.
func (f *Fake) SetMtlsStrip(on bool) { f.mu.Lock(); f.mtlsStrip = on; f.mu.Unlock() }

// MtlsCertificates returns the trusted CA set stored for a resource niceId.
func (f *Fake) MtlsCertificates(niceID string) []pangolin.MtlsCertificate {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.mtlsCerts[niceID]
}

func (f *Fake) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /v1/org/{orgId}/blueprint", f.handleApply)
	mux.HandleFunc("GET /v1/org/{orgId}/public-resources", f.handleList)
	mux.HandleFunc("GET /v1/org/{orgId}/private-resources", f.handlePrivateList)
	mux.HandleFunc("GET /v1/org/{orgId}/resource-policies", f.handlePolicyList)
	mux.HandleFunc("GET /v1/org/{orgId}/sites", f.handleSites)
	mux.HandleFunc("GET /v1/org/{orgId}/clients", f.handleMachines)
	mux.HandleFunc("GET /v1/org/{orgId}/users", f.handleUsers)
	mux.HandleFunc("GET /v1/public-resource/{id}/mtls/certificates", f.handleMtlsCertificates)
	mux.HandleFunc("DELETE /v1/public-resource/{id}", f.handleDelete)
	mux.HandleFunc("DELETE /v1/private-resource/{id}", f.handlePrivateDelete)
	mux.HandleFunc("DELETE /v1/resource-policy/{id}", f.handlePolicyDelete)

	// Control plane for e2e, where the fake runs as a pod and the test cannot
	// call the Go API directly.
	mux.HandleFunc("POST /_control/fail", f.handleControlFail)
	mux.HandleFunc("POST /_control/mtls-strip", f.handleControlMtlsStrip)
	mux.HandleFunc("POST /_control/licensed", f.handleControlLicensed)
	mux.HandleFunc("GET /_control/state", f.handleControlState)

	return logRequests(mux)
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Printf("pangolin-fake: %s %s\n", r.Method, r.URL.Path)
		next.ServeHTTP(w, r)
	})
}

func (f *Fake) handleApply(w http.ResponseWriter, r *http.Request) {
	if !f.authorised(w, r) {
		return
	}
	f.mu.Lock()
	if status := f.failApply; status != 0 {
		f.mu.Unlock()
		writeError(w, status, "apply failed on request")
		return
	}
	f.mu.Unlock()

	var body struct {
		Blueprint string `json:"blueprint"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "body is not JSON: "+err.Error())
		return
	}
	raw, err := base64.StdEncoding.DecodeString(body.Blueprint)
	if err != nil {
		writeError(w, http.StatusBadRequest, "blueprint is not base64: "+err.Error())
		return
	}
	var bp pangolin.Blueprint
	if err := json.Unmarshal(raw, &bp); err != nil {
		writeError(w, http.StatusBadRequest, "blueprint is not JSON: "+err.Error())
		return
	}

	// Pangolin's schema takes an array of rules or no key at all. A null fails
	// validation, which the wire format only avoids because Rules marshals a nil
	// slice as [].
	if key, bad := resourceWithNullRules(raw); bad {
		writeError(w, http.StatusBadRequest,
			fmt.Sprintf("Expected array, received null at \"public-resources.%s.rules\"", key))
		return
	}

	// An invalid rule value throws inside the same transaction as everything
	// else, so the whole document is rejected rather than partially applied.
	for _, key := range sortedKeys(bp.PublicResources) {
		if msg := checkRules(bp.PublicResources[key].Rules); msg != "" {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("%s (resource %s)", msg, key))
			return
		}
	}

	// Required fields Pangolin's schema refuses to default.
	for _, key := range sortedKeys(bp.PrivateResources) {
		res := bp.PrivateResources[key]
		switch {
		case res.Name == "":
			writeError(w, http.StatusBadRequest,
				fmt.Sprintf("Too small: expected string to have >=1 characters "+
					"at \"private-resources.%s.name\"", key))
			return
		case res.Mode == "":
			writeError(w, http.StatusBadRequest,
				fmt.Sprintf("Invalid option at \"private-resources.%s.mode\"", key))
			return
		// destination is required for every mode but ssh with a native
		// auth-daemon, and inference.
		case res.Destination == "":
			writeError(w, http.StatusBadRequest,
				fmt.Sprintf("destination is required unless mode is 'ssh' with auth-daemon "+
					"mode 'native', or mode is 'inference' (resource %s)", key))
			return
		}
		if msg := checkPrivateSchema(res); msg != "" {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("%s (resource %s)", msg, key))
			return
		}
	}

	// full-domain is unique per section: Pangolin checks proxy-resources and
	// client-resources against themselves. A real apply rejects the whole
	// document rather than applying it partially.
	seen := map[string]string{}
	for key, res := range bp.PublicResources {
		if res.FullDomain == "" {
			continue
		}
		if other, dup := seen[res.FullDomain]; dup {
			writeError(w, http.StatusBadRequest,
				fmt.Sprintf("Duplicate 'full-domain' values found: %s (%s, %s)",
					res.FullDomain, other, key))
			return
		}
		seen[res.FullDomain] = key
	}

	seenPrivate := map[string]string{}
	for _, key := range sortedKeys(bp.PrivateResources) {
		domain := bp.PrivateResources[key].FullDomain
		if domain == "" {
			continue
		}
		if other, dup := seenPrivate[domain]; dup {
			writeError(w, http.StatusBadRequest,
				fmt.Sprintf("Duplicate 'full-domain' values found: %s (%s, %s)",
					domain, other, key))
			return
		}
		seenPrivate[domain] = key
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	if msg := f.checkPolicies(bp); msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}

	// An unknown site makes the real apply throw inside its transaction, so the
	// whole document is rejected rather than partially applied.
	for key, res := range bp.PublicResources {
		for _, target := range res.Targets {
			if target.Site != "" && !f.knownSite(target.Site) {
				writeError(w, http.StatusBadRequest,
					fmt.Sprintf("Site not found: %s in org (resource %s)", target.Site, key))
				return
			}
		}
	}

	for _, key := range sortedKeys(bp.PrivateResources) {
		for _, site := range bp.PrivateResources[key].Sites {
			if site != "" && !f.knownSite(site) {
				writeError(w, http.StatusBadRequest,
					fmt.Sprintf("No valid sites found for private resource %s in org (site %s)",
						key, site))
				return
			}
		}
	}

	// Both throw inside the transaction. An alias belongs to one site resource
	// in the organisation, and a grant list resolving to no row at all ends in
	// an insert of nothing, which the database layer refuses.
	for _, key := range sortedKeys(bp.PrivateResources) {
		res := bp.PrivateResources[key]
		if res.Alias != "" {
			if other := f.aliasOwner(res.Alias, key, bp.PrivateResources); other != "" {
				writeError(w, http.StatusBadRequest,
					fmt.Sprintf("Alias %s already in use by another site resource in org (%s)",
						res.Alias, other))
				return
			}
		}
		if len(res.Machines) > 0 && !f.anyMachine(res.Machines) {
			writeError(w, http.StatusBadRequest, "values() must be called with at least one value")
			return
		}
		if len(res.Users) > 0 && !f.anyUser(res.Users) {
			writeError(w, http.StatusBadRequest, "values() must be called with at least one value")
			return
		}
	}

	// A bad CA (a private key, an unparseable cert, or more than 20) throws
	// inside the transaction, so the whole document is rejected. An open-source
	// build never parses them because the schema stripped the field.
	mtlsByKey := map[string][]pangolin.MtlsCertificate{}
	if !f.mtlsStrip {
		for _, key := range sortedKeys(bp.PublicResources) {
			res := bp.PublicResources[key]
			if res.Auth == nil || res.Auth.MTLS == nil {
				continue
			}
			certs, msg := parseMtlsCertificates(res.Auth.MTLS.CACertificates)
			if msg != "" {
				writeError(w, http.StatusBadRequest, fmt.Sprintf("%s (resource %s)", msg, key))
				return
			}
			mtlsByKey[key] = certs
		}
	}

	f.received = append(f.received, bp)
	// Additive without prune: this is exactly the gap the controller exists to close.
	for key, res := range bp.PublicResources {
		f.put(key, res)
	}
	// Declarative replace: the stored set becomes exactly what was sent.
	for key, certs := range mtlsByKey {
		f.mtlsCerts[key] = certs
	}
	for key, res := range bp.PrivateResources {
		f.putPrivate(key, res)
	}
	// Additive like the resources: an apply never deletes a policy.
	for key, policy := range bp.PublicPolicies {
		f.putPolicy(key, policy)
	}
	writeOK(w, http.StatusCreated, "Blueprint applied successfully", nil)
}

// putPrivate assumes the lock is held.
func (f *Fake) putPrivate(niceID string, r pangolin.PrivateResource) int {
	f.privateResources[niceID] = r
	if id, ok := f.privateIDs[niceID]; ok {
		return id
	}
	id := f.nextPrivateID
	f.nextPrivateID++
	f.privateIDs[niceID] = id
	return id
}

func (f *Fake) handleList(w http.ResponseWriter, r *http.Request) {
	if !f.authorised(w, r) {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()

	if status := f.failList; status != 0 {
		writeError(w, status, "listing failed on request")
		return
	}

	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	size, _ := strconv.Atoi(r.URL.Query().Get("pageSize"))
	if size < 1 {
		size = 20
	}

	niceIDs := make([]string, 0, len(f.resources))
	for k := range f.resources {
		niceIDs = append(niceIDs, k)
	}
	sortStrings(niceIDs)

	rows := make([]pangolin.Resource, 0, len(niceIDs))
	for _, n := range niceIDs {
		rows = append(rows, pangolin.Resource{ResourceID: f.ids[n], NiceID: n})
	}

	start := min((page-1)*size, len(rows))
	end := min(start+size, len(rows))

	writeOK(w, http.StatusOK, "Resources retrieved successfully", map[string]any{
		"resources": rows[start:end],
		"pagination": map[string]int{
			"total":    len(rows),
			"pageSize": size,
			"page":     page,
		},
	})
}

// handlePrivateList serves the private listing in Pangolin's own spelling: rows
// under siteResources, the numeric id named siteResourceId.
func (f *Fake) handlePrivateList(w http.ResponseWriter, r *http.Request) {
	if !f.authorised(w, r) {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()

	if status := f.failPrivateList; status != 0 {
		writeError(w, status, "private listing failed on request")
		return
	}

	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	size, _ := strconv.Atoi(r.URL.Query().Get("pageSize"))
	if size < 1 {
		size = 20
	}

	rows := make([]map[string]any, 0, len(f.privateResources))
	for _, niceID := range sortedKeys(f.privateResources) {
		rows = append(rows, map[string]any{
			"siteResourceId": f.privateIDs[niceID],
			"niceId":         niceID,
		})
	}

	start := min((page-1)*size, len(rows))
	end := min(start+size, len(rows))

	writeOK(w, http.StatusOK, "Site resources retrieved successfully", map[string]any{
		"siteResources": rows[start:end],
		"pagination": map[string]int{
			"total":    len(rows),
			"pageSize": size,
			"page":     page,
		},
	})
}

// checkPolicies mirrors what Pangolin throws for inside the apply transaction:
// the licence check, the policy schema, and a resource naming a missing policy.
// It assumes the lock is held.
func (f *Fake) checkPolicies(bp pangolin.Blueprint) string {
	const unlicensed = "Your current subscription does not support shared resource policies. " +
		"Please upgrade to access this feature."

	for _, key := range sortedKeys(bp.PublicPolicies) {
		if !f.licensed {
			return unlicensed
		}
		if msg := checkPolicySchema(bp.PublicPolicies[key]); msg != "" {
			return fmt.Sprintf("%s (policy %s)", msg, key)
		}
	}
	for _, key := range sortedKeys(bp.PublicResources) {
		res := bp.PublicResources[key]
		if res.Auth != nil {
			for _, email := range res.Auth.WhitelistUsers {
				if !validEmail(email) {
					return fmt.Sprintf("Invalid email at \"public-resources.%s.auth.whitelist-users\" (%s)",
						key, email)
				}
			}
		}
		if res.Policy == "" {
			continue
		}
		if !f.licensed {
			return unlicensed
		}
		if _, inDoc := bp.PublicPolicies[res.Policy]; !inDoc {
			if _, stored := f.policies[res.Policy]; !stored {
				return fmt.Sprintf("Resource policy not found: %s (resource %s)", res.Policy, key)
			}
		}
	}
	return ""
}

func (f *Fake) handlePolicyList(w http.ResponseWriter, r *http.Request) {
	if !f.authorised(w, r) {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()

	if !f.licensed {
		writeError(w, http.StatusForbidden, "this feature requires a license")
		return
	}
	if status := f.failPolicyList; status != 0 {
		writeError(w, status, "policy listing failed on request")
		return
	}

	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	size, _ := strconv.Atoi(r.URL.Query().Get("pageSize"))
	if size < 1 {
		size = 20
	}

	rows := make([]map[string]any, 0, len(f.policies))
	for _, niceID := range sortedKeys(f.policies) {
		rows = append(rows, map[string]any{
			"resourcePolicyId": f.policyIDs[niceID],
			"name":             f.policies[niceID].Name,
			"niceId":           niceID,
			"orgId":            "test-org",
			"resources":        []any{},
		})
	}

	start := min((page-1)*size, len(rows))
	end := min(start+size, len(rows))

	writeOK(w, http.StatusOK, "Resource policies retrieved successfully", map[string]any{
		"policies": rows[start:end],
		"pagination": map[string]int{
			"total":    len(rows),
			"pageSize": size,
			"page":     page,
		},
	})
}

// handlePolicyDelete refuses while a resource still names the policy, as
// Pangolin does.
func (f *Fake) handlePolicyDelete(w http.ResponseWriter, r *http.Request) {
	if !f.authorised(w, r) {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()

	if !f.licensed {
		writeError(w, http.StatusForbidden, "this feature requires a license")
		return
	}
	if status := f.failPolicyDelete; status != 0 {
		writeError(w, status, "policy delete failed on request")
		return
	}

	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "resource policy id is not a number")
		return
	}

	for niceID, known := range f.policyIDs {
		if known != id {
			continue
		}
		for _, key := range sortedKeys(f.resources) {
			if f.resources[key].Policy == niceID {
				writeError(w, http.StatusConflict,
					"Cannot delete a resource policy that is still used by resource "+key)
				return
			}
		}
		delete(f.policies, niceID)
		delete(f.policyIDs, niceID)
		f.policyDeleted = append(f.policyDeleted, id)
		writeOK(w, http.StatusOK, "Resource policy deleted successfully", nil)
		return
	}
	writeError(w, http.StatusNotFound, "resource policy not found")
}

func (f *Fake) handleControlLicensed(w http.ResponseWriter, r *http.Request) {
	f.SetLicensed(r.URL.Query().Get("enabled") == "true")
	writeOK(w, http.StatusOK, "ok", nil)
}

func (f *Fake) handleSites(w http.ResponseWriter, r *http.Request) {
	if !f.authorised(w, r) {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()

	if status := f.failSites; status != 0 {
		writeError(w, status, "sites listing failed on request")
		return
	}

	rows := make([]pangolin.Site, 0, len(f.sites))
	for _, n := range f.sites {
		rows = append(rows, pangolin.Site{NiceID: n, Name: n})
	}
	writeOK(w, http.StatusOK, "Sites retrieved successfully", map[string]any{
		"sites": rows,
		"pagination": map[string]int{
			"total": len(rows), "pageSize": len(rows) + 1, "page": 1,
		},
	})
}

func (f *Fake) handleMachines(w http.ResponseWriter, r *http.Request) {
	if !f.authorised(w, r) {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()

	if status := f.failMachines; status != 0 {
		writeError(w, status, "clients listing failed on request")
		return
	}
	rows := make([]pangolin.Machine, 0, len(f.machines))
	for _, n := range f.machines {
		rows = append(rows, pangolin.Machine{NiceID: n, Name: n})
	}
	writeOK(w, http.StatusOK, "Clients retrieved successfully", map[string]any{
		"clients": rows,
		"pagination": map[string]int{
			"total": len(rows), "pageSize": len(rows) + 1, "page": 1,
		},
	})
}

func (f *Fake) handleUsers(w http.ResponseWriter, r *http.Request) {
	if !f.authorised(w, r) {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()

	if status := f.failUsers; status != 0 {
		writeError(w, status, "users listing failed on request")
		return
	}
	writeOK(w, http.StatusOK, "Users retrieved successfully", map[string]any{
		"users": f.users,
		"pagination": map[string]int{
			"total": len(f.users), "pageSize": len(f.users) + 1, "page": 1,
		},
	})
}

// aliasOwner returns another resource holding alias, in the stored set or in
// the document being applied. It assumes the lock is held.
func (f *Fake) aliasOwner(alias, key string, doc map[string]pangolin.PrivateResource) string {
	for _, other := range sortedKeys(doc) {
		if other != key && doc[other].Alias == alias {
			return other
		}
	}
	for _, other := range sortedKeys(f.privateResources) {
		if _, inDoc := doc[other]; inDoc || other == key {
			continue
		}
		if f.privateResources[other].Alias == alias {
			return other
		}
	}
	return ""
}

// anyMachine and anyUser assume the lock is held.
func (f *Fake) anyMachine(names []string) bool {
	for _, n := range names {
		for _, m := range f.machines {
			if n == m {
				return true
			}
		}
	}
	return false
}

func (f *Fake) anyUser(names []string) bool {
	for _, n := range names {
		for _, u := range f.users {
			if n == u.Username || n == u.Email {
				return true
			}
		}
	}
	return false
}

// knownSite assumes the lock is held.
func (f *Fake) knownSite(niceID string) bool {
	for _, s := range f.sites {
		if s == niceID {
			return true
		}
	}
	return false
}

func (f *Fake) handleDelete(w http.ResponseWriter, r *http.Request) {
	if !f.authorised(w, r) {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()

	if status := f.failDelete; status != 0 {
		writeError(w, status, "delete failed on request")
		return
	}

	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "resource id is not a number")
		return
	}

	for niceID, known := range f.ids {
		if known != id {
			continue
		}
		delete(f.resources, niceID)
		delete(f.ids, niceID)
		f.deleted = append(f.deleted, id)
		writeOK(w, http.StatusOK, "Resource deleted successfully", nil)
		return
	}
	writeError(w, http.StatusNotFound, "resource not found")
}

func (f *Fake) handlePrivateDelete(w http.ResponseWriter, r *http.Request) {
	if !f.authorised(w, r) {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()

	if status := f.failPrivateDelete; status != 0 {
		writeError(w, status, "private delete failed on request")
		return
	}

	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "site resource id is not a number")
		return
	}

	for niceID, known := range f.privateIDs {
		if known != id {
			continue
		}
		delete(f.privateResources, niceID)
		delete(f.privateIDs, niceID)
		f.privateDeleted = append(f.privateDeleted, id)
		writeOK(w, http.StatusOK, "Site resource deleted successfully", nil)
		return
	}
	writeError(w, http.StatusNotFound, "site resource not found")
}

func (f *Fake) handleMtlsCertificates(w http.ResponseWriter, r *http.Request) {
	if !f.authorised(w, r) {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()

	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "resource id is not a number")
		return
	}
	for niceID, known := range f.ids {
		if known != id {
			continue
		}
		certs := f.mtlsCerts[niceID]
		if certs == nil {
			certs = []pangolin.MtlsCertificate{}
		}
		writeOK(w, http.StatusOK, "ok", map[string]any{"certificates": certs})
		return
	}
	writeError(w, http.StatusNotFound, "resource not found")
}

func (f *Fake) handleControlMtlsStrip(w http.ResponseWriter, r *http.Request) {
	f.SetMtlsStrip(r.URL.Query().Get("enabled") == "true")
	writeOK(w, http.StatusOK, "ok", nil)
}

func (f *Fake) handleControlFail(w http.ResponseWriter, r *http.Request) {
	status, _ := strconv.Atoi(r.URL.Query().Get("status"))
	switch r.URL.Query().Get("on") {
	case "apply":
		f.FailApply(status)
	case "list":
		f.FailList(status)
	case "delete":
		f.FailDelete(status)
	case "private-list":
		f.FailPrivateList(status)
	case "private-delete":
		f.FailPrivateDelete(status)
	case "policy-list":
		f.FailPolicyList(status)
	case "policy-delete":
		f.FailPolicyDelete(status)
	case "machines":
		f.FailMachines(status)
	case "users":
		f.FailUsers(status)
	default:
		writeError(w, http.StatusBadRequest,
			"on must be apply, list, delete, private-list, private-delete, policy-list, policy-delete, machines or users")
		return
	}
	writeOK(w, http.StatusOK, "ok", nil)
}

func (f *Fake) handleControlState(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	writeOK(w, http.StatusOK, "ok", map[string]any{
		"resources":        f.resources,
		"privateResources": f.privateResources,
		"publicPolicies":   f.policies,
		"policyDeleted":    f.policyDeleted,
		"mtlsCertificates": f.mtlsCerts,
		"applies":          len(f.received),
		"deleted":          f.deleted,
		"privateDeleted":   f.privateDeleted,
	})
}

// authorised mirrors the real API closely enough to catch a missing or malformed
// key, which is the most likely misconfiguration in the field.
func (f *Fake) authorised(w http.ResponseWriter, r *http.Request) bool {
	auth := r.Header.Get("Authorization")
	key, ok := strings.CutPrefix(auth, "Bearer ")
	if !ok || !strings.Contains(key, ".") {
		writeError(w, http.StatusUnauthorized,
			"expected Authorization: Bearer <key_id>.<key_secret>")
		return false
	}
	return true
}

func writeOK(w http.ResponseWriter, status int, message string, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"data":    data,
		"success": true,
		"error":   false,
		"message": message,
		"status":  status,
	})
}

func writeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"data":    nil,
		"success": false,
		"error":   true,
		"message": message,
		"status":  status,
	})
}

// parseMtlsCertificates mirrors how Pangolin ingests a trusted CA set: it splits
// PEM bundles, rejects a private key or an unparseable cert, dedupes by SHA-256
// fingerprint, and caps the set at 20. An empty message means it is valid.
func parseMtlsCertificates(pems []string) ([]pangolin.MtlsCertificate, string) {
	var out []pangolin.MtlsCertificate
	seen := map[string]bool{}
	for _, entry := range pems {
		rest := []byte(entry)
		for {
			block, next := pem.Decode(rest)
			if block == nil {
				break
			}
			rest = next
			if strings.Contains(block.Type, "PRIVATE KEY") {
				return nil, "Upload the CA's public certificate only"
			}
			if block.Type != "CERTIFICATE" {
				continue
			}
			cert, err := x509.ParseCertificate(block.Bytes)
			if err != nil {
				return nil, "ca-certificate is not a valid certificate"
			}
			fp := fingerprint256(cert.Raw)
			if seen[fp] {
				continue
			}
			seen[fp] = true
			out = append(out, pangolin.MtlsCertificate{
				Fingerprint: fp,
				Subject:     cert.Subject.String(),
				NotAfter:    cert.NotAfter.Format(time.RFC3339),
			})
		}
	}
	if len(out) > 20 {
		return nil, "too many mTLS certificates (max 20)"
	}
	return out, ""
}

// fingerprint256 formats a SHA-256 over the certificate DER the way Pangolin's
// Node X509Certificate.fingerprint256 does: uppercase hex, colon-separated.
func fingerprint256(der []byte) string {
	sum := sha256.Sum256(der)
	hexed := strings.ToUpper(hex.EncodeToString(sum[:]))
	pairs := make([]string, 0, len(hexed)/2)
	for i := 0; i < len(hexed); i += 2 {
		pairs = append(pairs, hexed[i:i+2])
	}
	return strings.Join(pairs, ":")
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// resourceWithNullRules reports the first resource whose rules key is present
// but null, which Pangolin's schema rejects — it takes an array or nothing.
func resourceWithNullRules(raw []byte) (string, bool) {
	var doc struct {
		PublicResources map[string]map[string]json.RawMessage `json:"public-resources"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return "", false
	}

	for _, key := range sortedKeys(doc.PublicResources) {
		rules, present := doc.PublicResources[key]["rules"]
		if present && string(rules) == "null" {
			return key, true
		}
	}
	return "", false
}

// checkRules mirrors the validation Pangolin runs over a resource's rules and
// returns its complaint, or "" when they pass. It throws inside the apply
// transaction, so one bad value rejects the whole document — which is why the
// renderer checks these values first.
func checkRules(rules pangolin.Rules) string {
	priorities := map[int]bool{}

	for i, rule := range rules {
		switch rule.Action {
		case pangolin.ActionAllow, pangolin.ActionDeny, pangolin.ActionPass:
		default:
			return fmt.Sprintf("Invalid enum value for rules.%d.action: %q", i, rule.Action)
		}

		switch rule.Match {
		case pangolin.MatchIP:
			if net.ParseIP(rule.Value) == nil {
				return "Invalid IP provided: " + rule.Value
			}
		case pangolin.MatchCIDR:
			if _, _, err := net.ParseCIDR(rule.Value); err != nil {
				return "Invalid CIDR provided: " + rule.Value
			}
		case pangolin.MatchPath:
			if !validPathGlob(rule.Value) {
				return "Invalid URL glob pattern: " + rule.Value
			}
		case pangolin.MatchCountry, pangolin.MatchASN, pangolin.MatchRegion:
			// A real instance checks these against MaxMind databases it may not
			// have, which no API reports. The fake accepts them, as an instance
			// with the databases in place would.
		default:
			return fmt.Sprintf("Invalid enum value for rules.%d.match: %q", i, rule.Match)
		}

		// Pangolin assigns an unset priority from the index and refuses a
		// resource whose priorities collide.
		if priorities[i+1] {
			return "Rules have conflicting or invalid priorities"
		}
		priorities[i+1] = true
	}
	return ""
}

// validPathGlob mirrors Pangolin's isValidUrlGlobPattern.
func validPathGlob(pattern string) bool {
	if pattern == "/" {
		return true
	}
	pattern = strings.TrimPrefix(pattern, "/")
	if pattern == "" {
		return false
	}

	segments := strings.Split(pattern, "/")
	for i, segment := range segments {
		if segment == "" && i != len(segments)-1 {
			return false
		}
		for j := 0; j < len(segment); j++ {
			if segment[j] == '%' && j+2 < len(segment) {
				if !isHex(segment[j+1]) || !isHex(segment[j+2]) {
					return false
				}
				j += 2
				continue
			}
			if !isPathChar(segment[j]) {
				return false
			}
		}
	}
	return true
}

func isHex(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}

func isPathChar(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	default:
		return strings.ContainsRune("-._~!$&'()*+,;#=@:", rune(c))
	}
}

// Pangolin's own patterns for a host destination and for an alias.
var (
	hostnamePattern = regexp.MustCompile(
		`^(?:[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?\.)*[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?$`)
	aliasPattern = regexp.MustCompile(
		`^(?:[a-zA-Z0-9*?](?:[a-zA-Z0-9*?-]{0,61}[a-zA-Z0-9*?])?\.)+[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?$`)
)

// checkPrivateSchema mirrors the refinements PrivateResourceSchema runs, any
// of which rejects the whole document. It returns "" when the entry passes.
func checkPrivateSchema(res pangolin.PrivateResource) string {
	switch res.Mode {
	case pangolin.ModeHTTP, pangolin.ModeHost, pangolin.ModeCIDR, "ssh", "inference":
	default:
		return fmt.Sprintf("Invalid option: expected one of host|cidr|http|ssh|inference, got %q", res.Mode)
	}
	for _, role := range res.Roles {
		if role == "Admin" {
			return "Admin role cannot be included in roles"
		}
	}
	if res.Alias != "" && !aliasPattern.MatchString(res.Alias) {
		return "Alias must be a fully qualified domain name with optional wildcards"
	}
	for _, ports := range []*string{res.TCPPorts, res.UDPPorts} {
		if ports != nil && !validPortRanges(*ports) {
			return "Invalid port range string: " + *ports
		}
	}
	switch res.Mode {
	case pangolin.ModeHost:
		isIP := net.ParseIP(res.Destination) != nil
		validDomain := hostnamePattern.MatchString(res.Destination) &&
			res.Alias != "" && hostnamePattern.MatchString(res.Alias)
		if !isIP && !validDomain {
			return "Destination must be a valid IP address or valid domain AND alias is required"
		}
	case pangolin.ModeCIDR:
		if _, _, err := net.ParseCIDR(res.Destination); err != nil {
			return "Destination must be a valid CIDR notation for cidr mode"
		}
	}
	return ""
}

// validPortRanges mirrors portRangeStringSchema.
func validPortRanges(raw string) bool {
	if s := strings.TrimSpace(raw); s == "" || s == "*" {
		return true
	}
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			return false
		}
		lo, hi, isRange := strings.Cut(part, "-")
		if !isRange {
			hi = lo
		}
		start, err1 := strconv.Atoi(strings.TrimSpace(lo))
		end, err2 := strconv.Atoi(strings.TrimSpace(hi))
		if err1 != nil || err2 != nil || start < 1 || end > 65535 || start > end {
			return false
		}
	}
	return true
}

// sortedKeys keeps the fake's complaint about a document with several problems
// the same on every apply, so a test can assert on the message.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sortStrings(keys)
	return keys
}

// zod's email pattern has two lookaheads, expressed here as prefix and substring
// checks. The wildcard form is the one a policy whitelist adds.
var (
	emailPattern = regexp.MustCompile(
		`^[A-Za-z0-9_'+\-.]*[A-Za-z0-9_+-]@(?:[A-Za-z0-9][A-Za-z0-9-]*\.)+[A-Za-z]{2,}$`)
	wildcardEmailPattern = regexp.MustCompile(`^\*@[\w.-]+\.[a-zA-Z]{2,}$`)
	pincodePattern       = regexp.MustCompile(`^\d{6}$`)
)

func validEmail(s string) bool {
	return !strings.HasPrefix(s, ".") && !strings.Contains(s, "..") && emailPattern.MatchString(s)
}

// checkPolicySchema mirrors ResourcePolicySchema's refinements and returns its
// complaint, or "" when the policy passes.
func checkPolicySchema(p pangolin.PublicPolicy) string {
	switch {
	case p.Name == "" || len(p.Name) > 255:
		return "Too small: expected string to have >=1 characters at \"name\""
	case p.Pincode != "" && !pincodePattern.MatchString(p.Pincode):
		return "Invalid string: must match pattern /^\\d{6}$/ at \"pincode\""
	}
	for _, role := range p.SSORoles {
		if role == "Admin" {
			return "Admin role cannot be included in sso-roles"
		}
	}
	for _, entry := range p.WhitelistUsers {
		if !validEmail(entry) && !wildcardEmailPattern.MatchString(entry) {
			return "Invalid email or domain wildcard at \"whitelist-users\": " + entry
		}
	}
	return checkRules(p.Rules)
}
