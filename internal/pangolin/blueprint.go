// SPDX-License-Identifier: AGPL-3.0-only

// Package pangolin talks to a Pangolin instance's Integration API.
package pangolin

// Resource modes. Pangolin also accepts a deprecated "protocol" key carrying the
// same values; this client only ever sends "mode".
const (
	ModeHTTP = "http"
)

// Target methods, the scheme Pangolin uses to reach a backend.
const (
	MethodHTTP = "http"
)

// Blueprint is the desired-state document sent to the Integration API. Only the
// sections this controller owns are modelled; Pangolin leaves absent sections alone.
type Blueprint struct {
	PublicResources map[string]PublicResource `json:"public-resources"`
}

// PublicResource is one entry under public-resources, keyed by its niceId.
type PublicResource struct {
	Name       string   `json:"name"`
	Mode       string   `json:"mode"`
	FullDomain string   `json:"full-domain,omitempty"`
	Auth       *Auth    `json:"auth,omitempty"`
	Targets    []Target `json:"targets,omitempty"`
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
