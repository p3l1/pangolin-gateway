// SPDX-License-Identifier: AGPL-3.0-only

package envtest_test

import (
	"os"
	"path/filepath"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
	"sigs.k8s.io/yaml"

	"github.com/p3l1/pangolin-gateway/api/v1alpha1"
	"github.com/p3l1/pangolin-gateway/internal/gateway"
	"github.com/p3l1/pangolin-gateway/test/envtestenv"
)

func TestEnvironmentServesCoreAPI(t *testing.T) {
	c, ctx := envtestenv.Start(t)

	var namespaces corev1.NamespaceList
	if err := c.List(ctx, &namespaces); err != nil {
		t.Fatalf("listing namespaces: %v", err)
	}
	if len(namespaces.Items) == 0 {
		t.Error("no namespaces returned; the API server is not serving core types")
	}
}

// The Gateway API CRDs come from the module cache, this project's own from
// config/crd; this asserts both are wired in.
func TestEnvironmentServesGatewayAPI(t *testing.T) {
	c, ctx := envtestenv.Start(t)

	for _, list := range []client.ObjectList{
		&gatewayv1.HTTPRouteList{},
		&gatewayv1.GatewayList{},
		&gatewayv1.GatewayClassList{},
		&v1alpha1.PrivateResourceList{},
	} {
		if err := c.List(ctx, list); err != nil {
			t.Errorf("listing %T: %v", list, err)
		}
	}
}

// The examples are documentation people copy, so each must pass the CRD schema
// and render without a rejection.
func TestPrivateResourceExamplesAreValid(t *testing.T) {
	c, ctx := envtestenv.Start(t)

	files, err := filepath.Glob("../../examples/privateresource-*.yaml")
	if err != nil || len(files) == 0 {
		t.Fatalf("no PrivateResource examples found (err %v)", err)
	}

	var objs []v1alpha1.PrivateResource
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("reading %s: %v", file, err)
		}
		var pr v1alpha1.PrivateResource
		if err := yaml.UnmarshalStrict(raw, &pr); err != nil {
			t.Fatalf("decoding %s: %v", file, err)
		}

		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: pr.Namespace}}
		if err := c.Create(ctx, ns); err != nil && !apierrors.IsAlreadyExists(err) {
			t.Fatalf("creating namespace %s: %v", pr.Namespace, err)
		}
		if err := c.Create(ctx, &pr); err != nil {
			t.Errorf("%s: the API server refused it: %v", file, err)
			continue
		}
		objs = append(objs, pr)
	}

	result := gateway.RenderAll(gateway.Inputs{
		DefaultSite:      "my-site",
		PrivateResources: objs,
	})
	for _, pr := range objs {
		v := result.PrivateResources[types.NamespacedName{Namespace: pr.Namespace, Name: pr.Name}]
		if !v.Published() {
			t.Errorf("%s/%s was rejected: %s", pr.Namespace, pr.Name, v.Accepted.Message)
		}
	}
}
