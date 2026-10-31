// SPDX-License-Identifier: AGPL-3.0-only

package gateway

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/p3l1/pangolin-gateway/api/v1alpha1"
	"github.com/p3l1/pangolin-gateway/internal/pangolin"
)

// AnnotationPolicy names a ResourcePolicy in the route's namespace. The policy
// then decides who may reach the route, so every annotation that would
// add a second opinion is refused.
const AnnotationPolicy = "pangolin.p3l1.de/policy"

// ReasonNotLicensed marks a ResourcePolicy the controller refuses to publish
// because shared policies are a licensed Pangolin feature.
const ReasonNotLicensed = "NotLicensed"

var pincodePattern = regexp.MustCompile(`^[0-9]{6}$`)

// PolicyKey is the blueprint key for a ResourcePolicy. Policies live in their
// own Pangolin table, so it cannot collide with a route's key.
func PolicyKey(namespace, name string) string {
	return KeyPrefix + namespace + "-" + name
}

// policyConflicts are the annotations a route cannot combine with a policy.
var policyConflicts = append([]string{
	AnnotationSSO, AnnotationPincode, AnnotationBasicAuth, AnnotationAccessRules,
	AnnotationRoles, AnnotationUsers, AnnotationEmailWhitelist,
}, mtlsAnnotations...)

// policyFor returns the key of the policy a route references, or "" for a route
// that names none. It refuses unless the controller is licensed, the policy is
// accepted, and the route carries no annotation of its own about access.
func policyFor(
	r gatewayv1.HTTPRoute,
	in Inputs,
	policies map[types.NamespacedName]ResourceVerdict,
) (string, error) {
	name := strings.TrimSpace(r.Annotations[AnnotationPolicy])
	if name == "" {
		return "", nil
	}
	if !in.Licensed {
		return "", fmt.Errorf("annotation %s needs shared policies, a licensed Pangolin feature; "+
			"start the controller with --pangolin-licensed", AnnotationPolicy)
	}
	for _, a := range policyConflicts {
		if strings.TrimSpace(r.Annotations[a]) != "" {
			return "", fmt.Errorf("annotation %s conflicts with %s; the policy decides who may "+
				"reach the route, so remove it", a, AnnotationPolicy)
		}
	}

	v, ok := policies[types.NamespacedName{Namespace: r.Namespace, Name: name}]
	switch {
	case !ok:
		return "", fmt.Errorf("ResourcePolicy %q does not exist in namespace %q", name, r.Namespace)
	case !v.Published():
		return "", fmt.Errorf("ResourcePolicy %q is not accepted: %s", name, v.Accepted.Message)
	}
	return v.Key, nil
}

// sharedPolicyAuth is the resource-level auth of a route in shared policy mode.
// Pincode, password and header auth fall back to the resource when the policy
// has none, so nothing may be sent here; mTLS still carries its clearing block.
func sharedPolicyAuth(r gatewayv1.HTTPRoute, in Inputs) (*pangolin.Auth, error) {
	mtls, err := mtlsFor(r, in)
	if err != nil {
		return nil, err
	}
	return &pangolin.Auth{SSOEnabled: false, MTLS: mtls}, nil
}

// renderPolicies translates every ResourcePolicy and awards each key to its
// oldest claimant, like the route collision check. Unlicensed, all are refused.
func renderPolicies(in Inputs) (
	map[types.NamespacedName]ResourceVerdict,
	map[string]pangolin.PublicPolicy,
) {
	verdicts := make(map[types.NamespacedName]ResourceVerdict, len(in.ResourcePolicies))
	entries := map[string]pangolin.PublicPolicy{}

	ordered := append([]v1alpha1.ResourcePolicy(nil), in.ResourcePolicies...)
	sort.SliceStable(ordered, func(i, j int) bool {
		a, b := ordered[i], ordered[j]
		switch {
		case !a.CreationTimestamp.Equal(&b.CreationTimestamp):
			return a.CreationTimestamp.Before(&b.CreationTimestamp)
		case a.Namespace != b.Namespace:
			return a.Namespace < b.Namespace
		default:
			return a.Name < b.Name
		}
	})

	owners := map[string]types.NamespacedName{}
	for _, rp := range ordered {
		name := types.NamespacedName{Namespace: rp.Namespace, Name: rp.Name}
		v := ResourceVerdict{Generation: rp.Generation, Accepted: resourceAccepted()}
		v.Accepted.Message = "Policy is valid and its key is unclaimed"

		policy, cond := translatePolicy(rp, in.Licensed)
		key := PolicyKey(rp.Namespace, rp.Name)
		if owner, taken := owners[key]; cond == nil && taken {
			c := ConditionResult{
				Status: metav1.ConditionFalse, Reason: ReasonDuplicateKey,
				Message: fmt.Sprintf("policy key %q is already claimed by ResourcePolicy %s", key, owner),
			}
			cond = &c
		}
		if cond != nil {
			v.Accepted = *cond
			verdicts[name] = v
			continue
		}
		owners[key] = name
		v.Key = key
		entries[key] = policy
		verdicts[name] = v
	}
	return verdicts, entries
}

func policyInvalid(format string, args ...any) (pangolin.PublicPolicy, *ConditionResult) {
	return pangolin.PublicPolicy{}, &ConditionResult{
		Status:  metav1.ConditionFalse,
		Reason:  ReasonInvalidSpec,
		Message: fmt.Sprintf(format, args...),
	}
}

// translatePolicy validates one ResourcePolicy. Pangolin rejects the whole
// document for anything it dislikes, so every check it runs is repeated here.
func translatePolicy(rp v1alpha1.ResourcePolicy, licensed bool) (pangolin.PublicPolicy, *ConditionResult) {
	spec := rp.Spec

	if !licensed {
		return pangolin.PublicPolicy{}, &ConditionResult{
			Status: metav1.ConditionFalse,
			Reason: ReasonNotLicensed,
			Message: "the controller is not configured as licensed; " +
				"shared policies need --pangolin-licensed and a licensed Pangolin",
		}
	}

	name := rp.Namespace + "/" + rp.Name
	if spec.DisplayName != "" {
		if strings.TrimSpace(spec.DisplayName) == "" {
			return policyInvalid("displayName is blank; remove it to fall back to %q", name)
		}
		name = strings.TrimSpace(spec.DisplayName)
	}

	sso := spec.SSO == nil || *spec.SSO
	if !sso && (len(spec.Roles) > 0 || len(spec.Users) > 0) {
		return policyInvalid("roles and users have no meaning with sso: false")
	}
	for _, l := range []struct {
		name    string
		entries []string
	}{{"roles", spec.Roles}, {"users", spec.Users}} {
		for _, e := range l.entries {
			if strings.TrimSpace(e) == "" {
				return policyInvalid("%s has an empty entry", l.name)
			}
		}
	}
	for _, role := range spec.Roles {
		if role == adminRole {
			return policyInvalid("roles must not name Admin; the admin role reaches every policy anyway")
		}
	}

	if spec.Pincode != "" && !pincodePattern.MatchString(spec.Pincode) {
		return policyInvalid("pincode must be exactly six digits")
	}

	whitelist, err := policyWhitelist(spec.EmailWhitelist)
	if err != nil {
		return policyInvalid("%s", err)
	}

	rules := make(pangolin.Rules, 0, len(spec.Rules))
	for i, s := range spec.Rules {
		rule, err := validRule(ruleSpec{Action: s.Action, Match: s.Match, Value: ruleValue(s.Value)})
		if err != nil {
			return policyInvalid("rules[%d]: %s", i, err)
		}
		rules = append(rules, rule)
	}

	return pangolin.PublicPolicy{
		Name:                  name,
		SSO:                   sso,
		SSORoles:              spec.Roles,
		SSOUsers:              spec.Users,
		Pincode:               spec.Pincode,
		EmailWhitelistEnabled: len(whitelist) > 0,
		WhitelistUsers:        whitelist,
		ApplyRules:            len(rules) > 0,
		Rules:                 rules,
	}, nil
}
