// SPDX-License-Identifier: AGPL-3.0-only

// Package pangolin talks to a Pangolin instance's Integration API.
package pangolin

import "encoding/json"

// Resource modes. Pangolin also accepts a deprecated "protocol" key carrying the
// same values; this client only ever sends "mode".
const (
	ModeHTTP = "http"
)

// Target methods, the scheme Pangolin uses to reach a backend.
const (
	MethodHTTP = "http"
)

// Schemes a private resource uses to reach its destination. Its public
// counterpart is a target's method.
const (
	SchemeHTTP = "http"
)

// Rule actions. Pangolin evaluates rules before any authentication: "allow"
// returns immediately and bypasses SSO, "deny" blocks, and "pass" falls through
// to the authentication checks — the same as no rule matching at all.
const (
	ActionAllow = "allow"
	ActionDeny  = "deny"
	ActionPass  = "pass"
)

// Rule match types. Rationale: docs/access-rules.md.
const (
	MatchCIDR    = "cidr"
	MatchPath    = "path"
	MatchIP      = "ip"
	MatchCountry = "country"
	MatchASN     = "asn"
	MatchRegion  = "region"
)

// Blueprint is the desired-state document sent to the Integration API. Only the
// sections this controller owns are modelled; Pangolin leaves absent sections alone.
type Blueprint struct {
	PublicResources map[string]PublicResource `json:"public-resources"`

	// Merged into client-resources by Pangolin, the way public-resources is
	// merged into proxy-resources. Reachable only through a Pangolin client, so
	// it carries no auth block: access is granted by role and by user.
	PrivateResources map[string]PrivateResource `json:"private-resources"`
}

// PublicResource is one entry under public-resources, keyed by its niceId.
type PublicResource struct {
	Name       string   `json:"name"`
	Mode       string   `json:"mode"`
	FullDomain string   `json:"full-domain,omitempty"`
	Auth       *Auth    `json:"auth,omitempty"`
	Targets    []Target `json:"targets,omitempty"`

	// Never omitted. Pangolin switches rule evaluation on for a resource whose
	// rules array is non-empty and off when it is empty, but leaves the setting
	// untouched when the key is absent — so an absent key would keep evaluating
	// the rules of a route whose last rule was just removed.
	Rules Rules `json:"rules"`
}

// Rule is one access rule. Pangolin's schema also takes priority and enabled;
// neither is sent, because the order of this slice is the priority and a rule
// that should not apply is removed rather than disabled.
type Rule struct {
	Action string `json:"action"`
	Match  string `json:"match"`
	Value  string `json:"value"`
}

// Rules marshals to an empty array rather than null when unset. Pangolin's
// schema takes an array or nothing, never null, so a nil slice encoded the
// usual way would fail the entire apply.
type Rules []Rule

func (r Rules) MarshalJSON() ([]byte, error) {
	if r == nil {
		return []byte("[]"), nil
	}
	return json.Marshal([]Rule(r))
}

type Auth struct {
	SSOEnabled bool `json:"sso-enabled"`
}

type Target struct {
	Site        string       `json:"site,omitempty"`
	Method      string       `json:"method,omitempty"`
	Hostname    string       `json:"hostname"`
	Port        int32        `json:"port"`
	Healthcheck *Healthcheck `json:"healthcheck,omitempty"`
}

// Healthcheck stops Pangolin routing to a target that has stopped answering.
// Hostname and port are required by the schema and always mirror the target's,
// since a check against a different address would not describe this target.
type Healthcheck struct {
	Hostname string `json:"hostname"`
	Port     int32  `json:"port"`
	Path     string `json:"path"`
	Interval int    `json:"interval"`
	Timeout  int    `json:"timeout"`
}

// PrivateResource is one entry under private-resources, keyed by its niceId.
// Only mode http is modelled: the other modes describe a destination with port
// ranges and no Kubernetes workload behind it, which an HTTPRoute cannot say.
type PrivateResource struct {
	Name string `json:"name"`
	Mode string `json:"mode"`

	// The plural key. Pangolin still reads a singular "site" but has deprecated it.
	Sites []string `json:"sites,omitempty"`

	Destination     string `json:"destination,omitempty"`
	DestinationPort int32  `json:"destination-port,omitempty"`
	FullDomain      string `json:"full-domain,omitempty"`
	Scheme          string `json:"scheme,omitempty"`

	// Pangolin forces tcp-ports, udp-ports, disable-icmp and ssl for mode http,
	// so sending them would only restate what it decides anyway.

	Roles []string `json:"roles,omitempty"`
	Users []string `json:"users,omitempty"`
}

// Site is one row of the sites listing. Only the niceId matters here: it is what
// a blueprint target names, and an unknown one fails the entire apply.
type Site struct {
	NiceID string `json:"niceId"`
	Name   string `json:"name"`
}

// Resource is one row of the public-resources listing. Only the two fields the
// prune needs are modelled: DELETE takes the numeric id, the blueprint the niceId.
type Resource struct {
	ResourceID int    `json:"resourceId"`
	NiceID     string `json:"niceId"`
}
