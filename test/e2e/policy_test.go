// SPDX-License-Identifier: AGPL-3.0-only

package e2e

import (
	"context"
	"fmt"
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/types"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/p3l1/pangolin-gateway/api/v1alpha1"
	"github.com/p3l1/pangolin-gateway/internal/gateway"
)

// The chart's ResourcePolicy CRD and RBAC, and --pangolin-licensed, are only
// exercised here: the policy must reach public-policies, the route must name it,
// and deleting both must prune the policy after the resource.
func TestResourcePolicyIsPublishedAndPruned(t *testing.T) {
	c := newClient(t)
	ctx := context.Background()

	waitForDeployment(t, c, deploymentName)
	waitForDeployment(t, c, fakeDeployment)

	gw := &gatewayv1.Gateway{}
	gw.Namespace = namespace
	gw.Name = "e2e-policy-gw"
	gw.Spec.GatewayClassName = "pangolin"
	gw.Spec.Listeners = []gatewayv1.Listener{{
		Name: "http", Port: 80, Protocol: gatewayv1.HTTPProtocolType,
	}}
	if err := c.Create(ctx, gw); err != nil {
		t.Fatalf("creating Gateway: %v", err)
	}
	t.Cleanup(func() { _ = c.Delete(context.Background(), gw) })

	policy := &v1alpha1.ResourcePolicy{}
	policy.Namespace = namespace
	policy.Name = "e2e-choir"
	policy.Spec = v1alpha1.ResourcePolicySpec{
		DisplayName:    "E2E Choir",
		EmailWhitelist: []string{"anna@example.com", "*@plan-b-chor.de"},
		Pincode:        "012345",
	}
	if err := c.Create(ctx, policy); err != nil {
		t.Fatalf("creating ResourcePolicy: %v", err)
	}
	t.Cleanup(func() { _ = c.Delete(context.Background(), policy) })

	port := gatewayv1.PortNumber(8080)
	route := &gatewayv1.HTTPRoute{}
	route.Namespace = namespace
	route.Name = "e2e-policy"
	route.Annotations = map[string]string{gateway.AnnotationPolicy: "e2e-choir"}
	route.Spec.ParentRefs = []gatewayv1.ParentReference{{Name: "e2e-policy-gw"}}
	route.Spec.Hostnames = []gatewayv1.Hostname{"e2e-policy.example.com"}
	route.Spec.Rules = []gatewayv1.HTTPRouteRule{{
		BackendRefs: []gatewayv1.HTTPBackendRef{{
			BackendRef: gatewayv1.BackendRef{
				BackendObjectReference: gatewayv1.BackendObjectReference{Name: "e2e-policy", Port: &port},
			},
		}},
	}}
	if err := c.Create(ctx, route); err != nil {
		t.Fatalf("creating HTTPRoute: %v", err)
	}
	t.Cleanup(func() { _ = c.Delete(context.Background(), route) })

	policyKey := "gw-" + namespace + "-e2e-choir"
	routeKey := "gw-" + namespace + "-e2e-policy"

	eachPoll(t, readyTimeout, func() error {
		state := readFakeState(t)
		p, ok := state.PublicPolicies[policyKey]
		if !ok {
			return fmt.Errorf("%s not published yet; fake holds %v", policyKey, keysOf(state.PublicPolicies))
		}
		if p["pincode"] != "012345" || p["email-whitelist-enabled"] != true {
			return fmt.Errorf("policy entry = %v", p)
		}
		r, ok := state.Resources[routeKey]
		if !ok {
			return fmt.Errorf("%s not published yet", routeKey)
		}
		if r["policy"] != policyKey {
			return fmt.Errorf("route entry = %v, want policy %s", r, policyKey)
		}
		return nil
	})

	eachPoll(t, readyTimeout, func() error {
		var live v1alpha1.ResourcePolicy
		if err := c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: policy.Name}, &live); err != nil {
			return err
		}
		programmed := meta.FindStatusCondition(live.Status.Conditions, v1alpha1.ConditionProgrammed)
		if programmed == nil || programmed.Status != "True" {
			return fmt.Errorf("Programmed = %+v, want True", programmed)
		}
		if live.Status.PolicyKey != policyKey {
			return fmt.Errorf("policyKey = %q, want %q", live.Status.PolicyKey, policyKey)
		}
		return nil
	})

	before := len(readFakeState(t).PolicyDeleted)
	if err := c.Delete(ctx, route); err != nil {
		t.Fatalf("deleting HTTPRoute: %v", err)
	}
	if err := c.Delete(ctx, policy); err != nil {
		t.Fatalf("deleting ResourcePolicy: %v", err)
	}

	eachPoll(t, readyTimeout, func() error {
		state := readFakeState(t)
		if _, ok := state.PublicPolicies[policyKey]; ok {
			return fmt.Errorf("%s survived its ResourcePolicy", policyKey)
		}
		if len(state.PolicyDeleted) <= before {
			return fmt.Errorf("no DELETE reached the policy endpoint")
		}
		return nil
	})
}
