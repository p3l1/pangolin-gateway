// SPDX-License-Identifier: AGPL-3.0-only

package gateway

import (
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/p3l1/pangolin-gateway/api/v1alpha1"
	"github.com/p3l1/pangolin-gateway/internal/pangolin"
)

// PrivateResourceKeyPrefix keeps a PrivateResource's key apart from that of an
// HTTPRoute with the same namespace and name. It extends KeyPrefix, so the
// prune treats both alike.
const PrivateResourceKeyPrefix = KeyPrefix + "pr-"

// Reasons for a PrivateResource's conditions. Collisions and unknown sites
// reuse the route reasons above.
const (
	ReasonAccepted       = "Accepted"
	ReasonInvalidSpec    = "InvalidSpec"
	ReasonUnknownMachine = "UnknownMachine"
	ReasonUnknownUser    = "UnknownUser"
	ReasonProgrammed     = "Programmed"
	ReasonNotAccepted    = "NotAccepted"
)

// adminRole is refused by Pangolin's schema for the whole document. The role
// keeps access to every private resource regardless.
const adminRole = "Admin"

// Pangolin's own patterns for a destination hostname and for an alias, which
// may carry * and ? wildcards and needs at least one dot.
var (
	hostnamePattern = regexp.MustCompile(
		`^(?:[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?\.)*[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?$`)
	aliasPattern = regexp.MustCompile(
		`^(?:[a-zA-Z0-9*?](?:[a-zA-Z0-9*?-]{0,61}[a-zA-Z0-9*?])?\.)+[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?$`)
)

// ResourceVerdict is what the renderer concluded about one PrivateResource.
// Programmed is not part of it: only the apply can decide that.
type ResourceVerdict struct {
	Key        string
	Generation int64
	Accepted   ConditionResult
}

// Published reports whether the resource made it into the blueprint.
func (v ResourceVerdict) Published() bool { return v.Key != "" }

// PrivateResourceKey is the blueprint key for a PrivateResource.
func PrivateResourceKey(namespace, name string) string {
	return PrivateResourceKeyPrefix + namespace + "-" + name
}

func resourceAccepted() ConditionResult {
	return ConditionResult{
		Status:  metav1.ConditionTrue,
		Reason:  ReasonAccepted,
		Message: "Resource is valid and its names are unclaimed",
	}
}

func invalid(format string, args ...any) (rendered, *ConditionResult) {
	c := ConditionResult{
		Status:  metav1.ConditionFalse,
		Reason:  ReasonInvalidSpec,
		Message: fmt.Sprintf(format, args...),
	}
	return rendered{}, &c
}

// translatePrivateResource validates one PrivateResource and builds its entry.
// Anything Pangolin would reject is caught here, because Pangolin rejects the
// whole document for it and so unpublishes every other resource's changes too.
func translatePrivateResource(pr v1alpha1.PrivateResource, in Inputs) (rendered, *ConditionResult) {
	spec := pr.Spec

	name := pr.Namespace + "/" + pr.Name
	if spec.DisplayName != "" {
		if strings.TrimSpace(spec.DisplayName) == "" {
			return invalid("displayName is blank; remove it to fall back to %q", name)
		}
		name = strings.TrimSpace(spec.DisplayName)
	}

	sites := spec.Sites
	if len(sites) == 0 {
		sites = []string{in.DefaultSite}
	}
	for _, site := range sites {
		if strings.TrimSpace(site) == "" {
			return invalid("sites has an empty entry")
		}
		if in.KnownSites != nil && !in.KnownSites[site] {
			c := ConditionResult{
				Status:  metav1.ConditionFalse,
				Reason:  ReasonUnknownSite,
				Message: fmt.Sprintf("site %q does not exist in this Pangolin organisation", site),
			}
			return rendered{}, &c
		}
	}

	out := pangolin.PrivateResource{
		Name:        name,
		Mode:        string(spec.Mode),
		Sites:       sites,
		Destination: spec.Destination,
	}
	entry := rendered{key: PrivateResourceKey(pr.Namespace, pr.Name)}

	switch spec.Mode {
	case v1alpha1.ModeHost, v1alpha1.ModeCIDR:
		if msg := refuseFields(spec.Mode, map[string]bool{
			"fullDomain":      spec.FullDomain != "",
			"ssl":             spec.SSL != nil,
			"scheme":          spec.Scheme != "",
			"destinationPort": spec.DestinationPort != 0,
		}); msg != "" {
			return invalid("%s", msg)
		}

		if spec.Mode == v1alpha1.ModeHost {
			isIP := net.ParseIP(spec.Destination) != nil
			if !isIP && !hostnamePattern.MatchString(spec.Destination) {
				return invalid("destination %q is neither an IP address nor a hostname", spec.Destination)
			}
			if !isIP && spec.Alias == "" {
				return invalid("alias is required when the destination %q is a hostname", spec.Destination)
			}
			if spec.Alias != "" && !aliasPattern.MatchString(spec.Alias) {
				return invalid("alias %q is not a fully qualified domain name", spec.Alias)
			}
			out.Alias = spec.Alias
			if spec.Alias != "" {
				entry.hostnames = []string{spec.Alias}
			}
		} else {
			if _, _, err := net.ParseCIDR(spec.Destination); err != nil {
				return invalid("destination %q is not a CIDR block", spec.Destination)
			}
			if spec.Alias != "" {
				return invalid("alias has no meaning in mode cidr; only host takes one")
			}
		}

		if err := checkPortRanges(spec.TCPPorts); err != nil {
			return invalid("tcpPorts: %s", err)
		}
		if err := checkPortRanges(spec.UDPPorts); err != nil {
			return invalid("udpPorts: %s", err)
		}
		out.TCPPorts = ptr(strings.TrimSpace(spec.TCPPorts))
		out.UDPPorts = ptr(strings.TrimSpace(spec.UDPPorts))
		out.DisableICMP = ptr(spec.DisableICMP)

	case v1alpha1.ModeHTTP:
		if msg := refuseFields(spec.Mode, map[string]bool{
			"tcpPorts":    spec.TCPPorts != "",
			"udpPorts":    spec.UDPPorts != "",
			"disableIcmp": spec.DisableICMP,
			"alias":       spec.Alias != "",
		}); msg != "" {
			return invalid("%s", msg)
		}

		switch {
		case spec.FullDomain == "":
			return invalid("fullDomain is required in mode http")
		case strings.HasPrefix(spec.FullDomain, "*"):
			return invalid("Pangolin has no wildcard resources; %q cannot be published", spec.FullDomain)
		case !hostnamePattern.MatchString(spec.FullDomain):
			return invalid("fullDomain %q is not a hostname", spec.FullDomain)
		case net.ParseIP(spec.Destination) == nil && !hostnamePattern.MatchString(spec.Destination):
			return invalid("destination %q is neither an IP address nor a hostname", spec.Destination)
		case spec.DestinationPort == 0:
			return invalid("destinationPort is required in mode http")
		}

		scheme := pangolin.SchemeHTTP
		if spec.Scheme != "" {
			scheme = string(spec.Scheme)
		}
		ssl := true
		if spec.SSL != nil {
			ssl = *spec.SSL
		}
		out.FullDomain = spec.FullDomain
		out.Scheme = scheme
		out.SSL = &ssl
		out.DestinationPort = spec.DestinationPort
		entry.hostnames = []string{spec.FullDomain}

	default:
		return invalid("mode %q is not supported; use host, cidr or http", spec.Mode)
	}

	if err := checkPrivateGrants(spec.Roles, spec.Users, spec.Machines, in); err != nil {
		c := ConditionResult{Status: metav1.ConditionFalse, Reason: err.reason, Message: err.msg}
		return rendered{}, &c
	}
	out.Roles = spec.Roles
	out.Users = spec.Users
	out.Machines = spec.Machines

	entry.private = &out
	return entry, nil
}

// refuseFields names the first set field the mode has no use for. Pangolin
// ignores or overrides them, so publishing would not do what the spec says.
func refuseFields(mode v1alpha1.Mode, set map[string]bool) string {
	for _, field := range []string{
		"fullDomain", "ssl", "scheme", "destinationPort", "tcpPorts", "udpPorts", "disableIcmp", "alias",
	} {
		if set[field] {
			return fmt.Sprintf("%s has no meaning in mode %s", field, mode)
		}
	}
	return ""
}

// grantError carries the condition reason with the message, so a route and a
// PrivateResource can report the same check each in its own terms.
type grantError struct {
	reason string
	msg    string
}

func (e *grantError) Error() string { return e.msg }

// checkPrivateGrants mirrors what Pangolin rejects the whole document for: the
// Admin role, and a users or machines list none of whose names resolve. It is
// stricter than that, refusing any single unknown name Pangolin would drop.
func checkPrivateGrants(roles, users, machines []string, in Inputs) *grantError {
	for _, l := range []struct {
		name    string
		entries []string
	}{{"roles", roles}, {"users", users}, {"machines", machines}} {
		for _, e := range l.entries {
			if strings.TrimSpace(e) == "" {
				return &grantError{ReasonInvalidSpec, l.name + " has an empty entry"}
			}
		}
	}
	for _, r := range roles {
		if r == adminRole {
			return &grantError{ReasonInvalidSpec,
				"roles must not name Admin; the admin role reaches every private resource anyway"}
		}
	}
	if in.KnownUsers != nil {
		for _, u := range users {
			if !in.KnownUsers[u] {
				return &grantError{ReasonUnknownUser,
					fmt.Sprintf("user %q is neither a username nor an e-mail address in this organisation", u)}
			}
		}
	}
	if in.KnownMachines != nil {
		for _, m := range machines {
			if !in.KnownMachines[m] {
				return &grantError{ReasonUnknownMachine,
					fmt.Sprintf("machine client %q does not exist in this organisation", m)}
			}
		}
	}
	return nil
}

// checkPortRanges mirrors Pangolin's portRangeStringSchema: empty, "*", or a
// comma-separated list of ports and ranges between 1 and 65535.
func checkPortRanges(raw string) error {
	if s := strings.TrimSpace(raw); s == "" || s == "*" {
		return nil
	}
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			return fmt.Errorf("%q has an empty entry", raw)
		}
		lo, hi, isRange := strings.Cut(part, "-")
		start, err := portNumber(lo)
		if err != nil {
			return fmt.Errorf("%q: %w", raw, err)
		}
		end := start
		if isRange {
			if end, err = portNumber(hi); err != nil {
				return fmt.Errorf("%q: %w", raw, err)
			}
		}
		if start > end {
			return fmt.Errorf("%q: range %s starts after it ends", raw, part)
		}
	}
	return nil
}

func portNumber(s string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n < 1 || n > 65535 {
		return 0, fmt.Errorf("%q is not a port between 1 and 65535", strings.TrimSpace(s))
	}
	return n, nil
}

func ptr[T any](v T) *T { return &v }
