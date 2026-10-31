// SPDX-License-Identifier: AGPL-3.0-only

package gateway

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"

	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// AnnotationEmailWhitelist lists the e-mail addresses allowed to sign in with a
// one-time code, separated by commas or whitespace. It sits beside SSO like the
// PIN does, and an empty value asks for no whitelist.
const AnnotationEmailWhitelist = "pangolin.p3l1.de/email-whitelist"

// Pangolin validates an inline whitelist with zod's email pattern, which has two
// lookaheads Go's regexp lacks; validEmail checks those separately. A policy
// whitelist also takes the wildcard form below.
var (
	emailPattern = regexp.MustCompile(
		`^[A-Za-z0-9_'+\-.]*[A-Za-z0-9_+-]@(?:[A-Za-z0-9][A-Za-z0-9-]*\.)+[A-Za-z]{2,}$`)
	wildcardPattern = regexp.MustCompile(`^\*@[A-Za-z0-9_.-]+\.[A-Za-z]{2,}$`)
)

func validEmail(s string) bool {
	return !strings.HasPrefix(s, ".") && !strings.Contains(s, "..") && emailPattern.MatchString(s)
}

// emailWhitelistFor reads the inline whitelist off a route: lowercased,
// deduplicated, in the order written. A wildcard is refused because Pangolin's
// resource schema rejects it, and one bad entry fails the whole apply.
func emailWhitelistFor(r gatewayv1.HTTPRoute) ([]string, error) {
	raw := r.Annotations[AnnotationEmailWhitelist]

	var out []string
	seen := map[string]bool{}
	for _, field := range strings.FieldsFunc(raw, func(c rune) bool {
		return c == ',' || unicode.IsSpace(c)
	}) {
		entry := strings.ToLower(field)
		switch {
		case strings.HasPrefix(entry, "*"):
			return nil, fmt.Errorf("annotation %s cannot hold the wildcard %q; "+
				"a domain wildcard needs a ResourcePolicy and --pangolin-licensed",
				AnnotationEmailWhitelist, field)
		case !validEmail(entry):
			return nil, fmt.Errorf("annotation %s entry %q is not an e-mail address",
				AnnotationEmailWhitelist, field)
		}
		if !seen[entry] {
			seen[entry] = true
			out = append(out, entry)
		}
	}
	return out, nil
}

// policyWhitelist normalises a ResourcePolicy's whitelist, which unlike the
// inline one may hold "*@domain" entries.
func policyWhitelist(entries []string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, e := range entries {
		entry := strings.ToLower(strings.TrimSpace(e))
		if !validEmail(entry) && !wildcardPattern.MatchString(entry) {
			return nil, fmt.Errorf("emailWhitelist entry %q is neither an e-mail address "+
				"nor a *@domain wildcard", e)
		}
		if !seen[entry] {
			seen[entry] = true
			out = append(out, entry)
		}
	}
	return out, nil
}
