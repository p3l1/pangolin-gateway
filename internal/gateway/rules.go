// SPDX-License-Identifier: AGPL-3.0-only

package gateway

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"

	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
	"sigs.k8s.io/yaml"

	"github.com/p3l1/pangolin-gateway/internal/pangolin"
)

// AnnotationAccessRules carries a YAML list of access rules. Rules are
// structured and ordered, which a flat annotation value cannot express, and
// HTTPRouteRule.matches cannot stand in for them: those select a backend, not
// who may reach it. Rationale: docs/access-rules.md.
const AnnotationAccessRules = "pangolin.p3l1.de/access-rules"

// ruleSpec is one entry as an author writes it. Priority and enabled are absent
// on purpose: Pangolin assigns priority by index and rejects a resource whose
// priorities collide, which a partly annotated list makes easy to trigger.
type ruleSpec struct {
	Action string    `json:"action"`
	Match  string    `json:"match"`
	Value  ruleValue `json:"value"`
}

// ruleValue mirrors Pangolin's own coercion: a region id or an ASN written as a
// bare YAML number is still a string on the wire.
type ruleValue string

func (v *ruleValue) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*v = ruleValue(s)
		return nil
	}
	if n := bytes.TrimSpace(b); len(n) > 0 && (n[0] == '-' || (n[0] >= '0' && n[0] <= '9')) {
		*v = ruleValue(n)
		return nil
	}
	return fmt.Errorf("value must be a string, got %s", b)
}

// rulesFor reads the access rules off a route. A nil result without an error
// means the route asked for none, which switches rule evaluation off.
func rulesFor(r gatewayv1.HTTPRoute) (pangolin.Rules, error) {
	raw, ok := r.Annotations[AnnotationAccessRules]
	if !ok || strings.TrimSpace(raw) == "" {
		return nil, nil
	}

	var specs []ruleSpec
	if err := yaml.UnmarshalStrict([]byte(raw), &specs); err != nil {
		return nil, fmt.Errorf("annotation %s must be a YAML list of rules, each with "+
			"action, match and value; list order is the priority: %w",
			AnnotationAccessRules, firstLine(err))
	}

	rules := make(pangolin.Rules, 0, len(specs))
	for i, s := range specs {
		rule, err := validRule(s)
		if err != nil {
			return nil, fmt.Errorf("annotation %s[%d]: %w", AnnotationAccessRules, i, err)
		}
		rules = append(rules, rule)
	}
	return rules, nil
}

func validRule(s ruleSpec) (pangolin.Rule, error) {
	switch s.Action {
	case pangolin.ActionAllow, pangolin.ActionDeny, pangolin.ActionPass:
	default:
		return pangolin.Rule{}, fmt.Errorf(
			"action %q is not one of %q, %q, %q; %q bypasses authentication for what it "+
				"matches, while %q only falls through to it",
			s.Action, pangolin.ActionAllow, pangolin.ActionDeny, pangolin.ActionPass,
			pangolin.ActionAllow, pangolin.ActionPass)
	}

	value := string(s.Value)
	if value == "" {
		return pangolin.Rule{}, errors.New("value is empty")
	}

	// Pangolin validates these two itself and fails the entire apply on a bad
	// one, so a single typo would unpublish every other route as well.
	switch s.Match {
	case pangolin.MatchIP:
		if net.ParseIP(value) == nil {
			return pangolin.Rule{}, fmt.Errorf("value %q is not an IP address", value)
		}
	case pangolin.MatchCIDR:
		if _, _, err := net.ParseCIDR(value); err != nil {
			return pangolin.Rule{}, fmt.Errorf("value %q is not a CIDR block", value)
		}
	case pangolin.MatchPath:
		if !validPathGlob(value) {
			return pangolin.Rule{}, fmt.Errorf(
				"value %q is not a valid path glob: segments may hold letters, digits, "+
					"%q, percent escapes and %q as a wildcard, and may not be empty",
				value, "-._~!$&'()+,;=@:#", "*")
		}
	case pangolin.MatchCountry, pangolin.MatchASN, pangolin.MatchRegion:
		// Checked by Pangolin against MaxMind databases this controller cannot
		// see. A value it rejects fails the whole apply; the README says so.
	default:
		return pangolin.Rule{}, fmt.Errorf("match %q is not one of %q, %q, %q, %q, %q, %q",
			s.Match, pangolin.MatchPath, pangolin.MatchCIDR, pangolin.MatchIP,
			pangolin.MatchCountry, pangolin.MatchASN, pangolin.MatchRegion)
	}

	return pangolin.Rule{Action: s.Action, Match: s.Match, Value: value}, nil
}

// validPathGlob mirrors Pangolin's own glob validator. It is deliberately
// stricter than its matcher, which also understands "?" — a value carrying one
// is rejected before it can fail the apply.
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
		// Only a trailing slash may produce an empty segment; "//" in the middle
		// is a typo rather than a pattern.
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

// firstLine keeps a decoder's multi-line complaint out of a status condition,
// where it would be truncated in the middle anyway.
func firstLine(err error) error {
	msg, _, _ := strings.Cut(err.Error(), "\n")
	return fmt.Errorf("%s", strings.TrimSpace(msg))
}
