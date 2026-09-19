// SPDX-License-Identifier: AGPL-3.0-only

// Package pangolinfake reproduces the three Integration API routes this
// controller uses, so envtest and e2e never reach a real Pangolin instance.
package pangolinfake

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"

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

	failApply  int
	failList   int
	failSites  int
	failDelete int
}

func New() *Fake {
	return &Fake{
		resources: map[string]pangolin.PublicResource{},
		ids:       map[string]int{},
		nextID:    1,
		// A real organisation always has at least one site; tests that care name
		// their own via AddSite.
		sites: []string{"test-site", "default-site"},
	}
}

// FailApply and friends make the next and all subsequent calls of that kind
// answer with the given status; zero restores normal behaviour.
func (f *Fake) FailApply(status int)  { f.mu.Lock(); f.failApply = status; f.mu.Unlock() }
func (f *Fake) FailList(status int)   { f.mu.Lock(); f.failList = status; f.mu.Unlock() }
func (f *Fake) FailDelete(status int) { f.mu.Lock(); f.failDelete = status; f.mu.Unlock() }
func (f *Fake) FailSites(status int)  { f.mu.Lock(); f.failSites = status; f.mu.Unlock() }

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

// Resources returns the resources the instance currently holds, keyed by niceId.
func (f *Fake) Resources() map[string]pangolin.PublicResource {
	f.mu.Lock()
	defer f.mu.Unlock()

	out := make(map[string]pangolin.PublicResource, len(f.resources))
	for k, v := range f.resources {
		out[k] = v
	}
	return out
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

func (f *Fake) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /v1/org/{orgId}/blueprint", f.handleApply)
	mux.HandleFunc("GET /v1/org/{orgId}/public-resources", f.handleList)
	mux.HandleFunc("GET /v1/org/{orgId}/sites", f.handleSites)
	mux.HandleFunc("DELETE /v1/public-resource/{id}", f.handleDelete)

	// Control plane for e2e, where the fake runs as a pod and the test cannot
	// call the Go API directly.
	mux.HandleFunc("POST /_control/fail", f.handleControlFail)
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

	// full-domain must be unique across the instance; a real apply rejects the
	// whole document rather than applying it partially.
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

	f.mu.Lock()
	defer f.mu.Unlock()

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

	f.received = append(f.received, bp)
	// Additive without prune: this is exactly the gap the controller exists to close.
	for key, res := range bp.PublicResources {
		f.put(key, res)
	}
	writeOK(w, http.StatusCreated, "Blueprint applied successfully", nil)
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

func (f *Fake) handleControlFail(w http.ResponseWriter, r *http.Request) {
	status, _ := strconv.Atoi(r.URL.Query().Get("status"))
	switch r.URL.Query().Get("on") {
	case "apply":
		f.FailApply(status)
	case "list":
		f.FailList(status)
	case "delete":
		f.FailDelete(status)
	default:
		writeError(w, http.StatusBadRequest, "on must be apply, list or delete")
		return
	}
	writeOK(w, http.StatusOK, "ok", nil)
}

func (f *Fake) handleControlState(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	writeOK(w, http.StatusOK, "ok", map[string]any{
		"resources": f.resources,
		"applies":   len(f.received),
		"deleted":   f.deleted,
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
