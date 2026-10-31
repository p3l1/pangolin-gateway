// SPDX-License-Identifier: AGPL-3.0-only

package gateway

import (
	"reflect"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/p3l1/pangolin-gateway/api/v1alpha1"
	"github.com/p3l1/pangolin-gateway/internal/pangolin"
)

func policyObj(namespace, name string, spec v1alpha1.ResourcePolicySpec) v1alpha1.ResourcePolicy {
	return v1alpha1.ResourcePolicy{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:         namespace,
			Name:              name,
			Generation:        2,
			CreationTimestamp: metav1.NewTime(created),
		},
		Spec: spec,
	}
}

func choirPolicy() v1alpha1.ResourcePolicy {
	return policyObj("demo", "choir", v1alpha1.ResourcePolicySpec{
		EmailWhitelist: []string{"Anna@Example.com", "*@Plan-B-Chor.de", "anna@example.com"},
		Pincode:        "012345",
		Roles:          []string{"Member"},
		Rules:          []v1alpha1.PolicyRule{{Action: "allow", Match: "path", Value: "/public/*"}},
	})
}

func licensedInputs(policies []v1alpha1.ResourcePolicy, routes ...gatewayv1.HTTPRoute) Inputs {
	in := defaultInputs(routes...)
	in.Licensed = true
	in.ResourcePolicies = policies
	return in
}

func policyVerdict(t *testing.T, res Result, ns, name string) ResourceVerdict {
	t.Helper()

	v, ok := res.Policies[types.NamespacedName{Namespace: ns, Name: name}]
	if !ok {
		t.Fatalf("no verdict for policy %s/%s; have %v", ns, name, res.Policies)
	}
	return v
}

func TestRenderMapsAResourcePolicyToAPublicPolicy(t *testing.T) {
	res := RenderAll(licensedInputs([]v1alpha1.ResourcePolicy{choirPolicy()}))

	got, ok := res.Blueprint.PublicPolicies["gw-demo-choir"]
	if !ok {
		t.Fatalf("policy not published; blueprint holds %v", res.Blueprint.PublicPolicies)
	}
	want := pangolin.PublicPolicy{
		Name:                  "demo/choir",
		SSO:                   true,
		SSORoles:              []string{"Member"},
		Pincode:               "012345",
		EmailWhitelistEnabled: true,
		WhitelistUsers:        []string{"anna@example.com", "*@plan-b-chor.de"},
		ApplyRules:            true,
		Rules:                 pangolin.Rules{{Action: "allow", Match: "path", Value: "/public/*"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("policy = %+v\nwant     %+v", got, want)
	}

	v := policyVerdict(t, res, "demo", "choir")
	if v.Key != "gw-demo-choir" || v.Accepted.Status != metav1.ConditionTrue || v.Generation != 2 {
		t.Errorf("verdict = %+v", v)
	}
}

func TestRenderHonoursAPolicyDisplayNameAndSSO(t *testing.T) {
	p := policyObj("demo", "open", v1alpha1.ResourcePolicySpec{
		DisplayName: " Open Choir ",
		SSO:         ptr(false),
		Pincode:     "123456",
	})

	got := RenderAll(licensedInputs([]v1alpha1.ResourcePolicy{p})).Blueprint.PublicPolicies["gw-demo-open"]
	if got.Name != "Open Choir" || got.SSO || got.EmailWhitelistEnabled || got.ApplyRules {
		t.Errorf("policy = %+v", got)
	}
}

func TestRenderRefusesEveryPolicyWhileUnlicensed(t *testing.T) {
	in := licensedInputs([]v1alpha1.ResourcePolicy{choirPolicy()})
	in.Licensed = false

	res := RenderAll(in)

	if len(res.Blueprint.PublicPolicies) != 0 {
		t.Errorf("an unlicensed controller emitted policies: %v", res.Blueprint.PublicPolicies)
	}
	v := policyVerdict(t, res, "demo", "choir")
	if v.Published() || v.Accepted.Status != metav1.ConditionFalse || v.Accepted.Reason != ReasonNotLicensed ||
		!strings.Contains(v.Accepted.Message, "not configured as licensed") {
		t.Errorf("verdict = %+v, want NotLicensed", v)
	}
}

func TestRenderRejectsAnInvalidResourcePolicy(t *testing.T) {
	for name, spec := range map[string]v1alpha1.ResourcePolicySpec{
		"blank display name": {DisplayName: "  "},
		"admin role":         {Roles: []string{"Admin"}},
		"empty role":         {Roles: []string{" "}},
		"empty user":         {Users: []string{""}},
		"grants without sso": {SSO: ptr(false), Roles: []string{"Member"}},
		"short pincode":      {Pincode: "12345"},
		"letter pincode":     {Pincode: "12345a"},
		"bad email":          {EmailWhitelist: []string{"nope"}},
		"bare wildcard":      {EmailWhitelist: []string{"*@"}},
		"wildcard tld":       {EmailWhitelist: []string{"*@example"}},
		"inner wildcard":     {EmailWhitelist: []string{"an*@example.com"}},
		"empty entry":        {EmailWhitelist: []string{""}},
		"bad rule action":    {Rules: []v1alpha1.PolicyRule{{Action: "maybe", Match: "ip", Value: "10.0.0.1"}}},
		"bad rule value":     {Rules: []v1alpha1.PolicyRule{{Action: "deny", Match: "ip", Value: "nope"}}},
	} {
		t.Run(name, func(t *testing.T) {
			res := RenderAll(licensedInputs([]v1alpha1.ResourcePolicy{
				policyObj("demo", "bad", spec), choirPolicy(),
			}))

			if _, ok := res.Blueprint.PublicPolicies["gw-demo-bad"]; ok {
				t.Fatal("an invalid policy was published")
			}
			v := policyVerdict(t, res, "demo", "bad")
			if v.Accepted.Status != metav1.ConditionFalse || v.Accepted.Reason != ReasonInvalidSpec {
				t.Errorf("verdict = %+v, want InvalidSpec", v.Accepted)
			}
			if _, ok := res.Blueprint.PublicPolicies["gw-demo-choir"]; !ok {
				t.Error("one invalid policy took the valid one down with it")
			}
		})
	}
}

// Hyphens are legal in both halves of the key, so two objects can claim one.
func TestRenderAwardsAPolicyKeyToItsOldestClaimant(t *testing.T) {
	older := policyObj("a-b", "c", v1alpha1.ResourcePolicySpec{})
	newer := policyObj("a", "b-c", v1alpha1.ResourcePolicySpec{})
	newer.CreationTimestamp = metav1.NewTime(created.Add(time.Hour))

	res := RenderAll(licensedInputs([]v1alpha1.ResourcePolicy{newer, older}))

	if v := policyVerdict(t, res, "a-b", "c"); !v.Published() {
		t.Errorf("the older policy lost its key: %+v", v)
	}
	v := policyVerdict(t, res, "a", "b-c")
	if v.Published() || v.Accepted.Reason != ReasonDuplicateKey {
		t.Errorf("verdict = %+v, want DuplicateResourceKey", v)
	}
}

func policyRoute(policy string, extra map[string]string) gatewayv1.HTTPRoute {
	kv := map[string]string{AnnotationPolicy: policy}
	for k, v := range extra {
		kv[k] = v
	}
	return route("demo", "web", withAnnotations(kv))
}

func TestRenderPutsARouteInSharedPolicyMode(t *testing.T) {
	in := licensedInputs([]v1alpha1.ResourcePolicy{choirPolicy()}, policyRoute("choir", nil))

	res := RenderAll(in)

	web, ok := res.Blueprint.PublicResources["gw-demo-web"]
	if !ok {
		t.Fatalf("route not published: %+v", res.Routes)
	}
	if web.Policy != "gw-demo-choir" {
		t.Errorf("policy = %q, want gw-demo-choir", web.Policy)
	}
	auth := web.Auth
	if auth.SSOEnabled || auth.Pincode != nil || auth.BasicAuth != nil || len(auth.SSORoles) != 0 ||
		len(auth.SSOUsers) != 0 || len(auth.WhitelistUsers) != 0 {
		t.Errorf("auth = %+v; nothing may leak through the fallback to the resource", auth)
	}
	if auth.MTLS == nil || auth.MTLS.Enabled {
		t.Errorf("mtls = %+v, want the disabled clearing block", auth.MTLS)
	}
	if len(web.Rules) != 0 {
		t.Errorf("rules = %v, want none", web.Rules)
	}
	if len(web.Targets) != 1 {
		t.Errorf("targets = %v, want the route's backend", web.Targets)
	}
}

func TestRenderRefusesAPolicyReferenceItCannotHonour(t *testing.T) {
	unaccepted := policyObj("demo", "broken", v1alpha1.ResourcePolicySpec{Pincode: "1"})
	privateRoute := policyRoute("choir", map[string]string{AnnotationVisibility: VisibilityPrivate})

	for name, tc := range map[string]struct {
		licensed bool
		route    gatewayv1.HTTPRoute
		wantIn   string
	}{
		"unlicensed":     {false, policyRoute("choir", nil), "--pangolin-licensed"},
		"missing policy": {true, policyRoute("ghost", nil), `"ghost" does not exist`},
		"unaccepted":     {true, policyRoute("broken", nil), `"broken" is not accepted`},
		"private route":  {true, privateRoute, "private route"},
	} {
		t.Run(name, func(t *testing.T) {
			in := licensedInputs([]v1alpha1.ResourcePolicy{choirPolicy(), unaccepted}, tc.route)
			in.Licensed = tc.licensed

			res := RenderAll(in)

			if _, ok := res.Blueprint.PublicResources["gw-demo-web"]; ok {
				t.Fatal("route was published")
			}
			if _, ok := res.Blueprint.PrivateResources["gw-demo-web"]; ok {
				t.Fatal("route was published privately")
			}
			v := verdictFor(t, res.Routes, "demo", "web")
			if v.Accepted.Status != metav1.ConditionFalse || v.Accepted.Reason != "UnsupportedValue" ||
				!strings.Contains(v.Accepted.Message, tc.wantIn) {
				t.Errorf("accepted = %+v, want UnsupportedValue mentioning %q", v.Accepted, tc.wantIn)
			}
		})
	}
}

func TestRenderRefusesAPolicyOnlyWithinTheRoutesNamespace(t *testing.T) {
	elsewhere := policyObj("other", "choir", v1alpha1.ResourcePolicySpec{})

	res := RenderAll(licensedInputs([]v1alpha1.ResourcePolicy{elsewhere}, policyRoute("choir", nil)))

	if _, ok := res.Blueprint.PublicResources["gw-demo-web"]; ok {
		t.Error("a route referenced a policy from another namespace")
	}
}

func TestRenderNamesTheAnnotationConflictingWithAPolicy(t *testing.T) {
	for annotation, value := range map[string]string{
		AnnotationSSO:             "true",
		AnnotationPincode:         "123456",
		AnnotationBasicAuth:       "true",
		AnnotationAccessRules:     "- {action: deny, match: ip, value: 10.0.0.1}",
		AnnotationRoles:           "Member",
		AnnotationUsers:           "anna",
		AnnotationEmailWhitelist:  "anna@example.com",
		AnnotationMTLSCA:          "pem",
		AnnotationMTLSCASecret:    "ca",
		AnnotationMTLSCAConfigMap: "ca",
	} {
		t.Run(annotation, func(t *testing.T) {
			in := licensedInputs([]v1alpha1.ResourcePolicy{choirPolicy()},
				policyRoute("choir", map[string]string{annotation: value}))

			res := RenderAll(in)

			if _, ok := res.Blueprint.PublicResources["gw-demo-web"]; ok {
				t.Fatal("route was published with a conflicting annotation")
			}
			v := verdictFor(t, res.Routes, "demo", "web")
			if v.Accepted.Reason != "UnsupportedValue" || !strings.Contains(v.Accepted.Message, annotation) {
				t.Errorf("accepted = %+v, want UnsupportedValue naming %s", v.Accepted, annotation)
			}
		})
	}
}

// A template writes every key whether or not it has a value to put there.
func TestRenderIgnoresEmptyAnnotationsBesideAPolicy(t *testing.T) {
	in := licensedInputs([]v1alpha1.ResourcePolicy{choirPolicy()},
		policyRoute("choir", map[string]string{AnnotationPincode: "", AnnotationEmailWhitelist: " "}))

	res := RenderAll(in)

	if _, ok := res.Blueprint.PublicResources["gw-demo-web"]; !ok {
		t.Errorf("route was refused: %+v", res.Routes)
	}
}

func TestRenderTreatsAnEmptyPolicyAnnotationAsNone(t *testing.T) {
	in := licensedInputs(nil, policyRoute("", map[string]string{AnnotationPincode: "123456"}))
	in.Licensed = false

	res := RenderAll(in)

	web, ok := res.Blueprint.PublicResources["gw-demo-web"]
	if !ok || web.Policy != "" || !web.Auth.SSOEnabled {
		t.Errorf("resource = %+v (present %t), want inline mode", web, ok)
	}
}

func TestRenderEmitsNoPolicyKeysWithoutPolicies(t *testing.T) {
	res := RenderAll(defaultInputs(route("demo", "web")))

	if len(res.Blueprint.PublicPolicies) != 0 {
		t.Errorf("public-policies = %v, want none", res.Blueprint.PublicPolicies)
	}
	if res.Blueprint.PublicResources["gw-demo-web"].Policy != "" {
		t.Error("a route without the annotation carries a policy")
	}
}
