// SPDX-License-Identifier: AGPL-3.0-only

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/p3l1/pangolin-gateway/internal/gateway"
)

const (
	kubeContext    = "k3d-pangolin-gateway"
	namespace      = "pangolin-gateway-system"
	deploymentName = "pangolin-gateway"
	fakeDeployment = "pangolin-fake"
	readyTimeout   = 3 * time.Minute
)

// newClient builds a client for kubeContext, with the Gateway API types registered.
func newClient(t *testing.T) client.Client {
	t.Helper()

	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		clientcmd.NewDefaultClientConfigLoadingRules(),
		&clientcmd.ConfigOverrides{CurrentContext: kubeContext},
	).ClientConfig()
	if err != nil {
		t.Fatalf("no kubeconfig for context %s: %v (run: just cluster-up)", kubeContext, err)
	}

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("registering client-go scheme: %v", err)
	}
	if err := gatewayv1.Install(scheme); err != nil {
		t.Fatalf("registering gateway-api scheme: %v", err)
	}

	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatalf("building client: %v", err)
	}
	return c
}

// eachPoll always calls fn at least once, even for a non-positive timeout, so a
// caller can't mistake "never checked" for "checked and failed".
func eachPoll(t *testing.T, timeout time.Duration, fn func() error) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for {
		err := fn()
		if err == nil {
			return
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("condition not met within %s: %v", timeout, err)
		}
		time.Sleep(2 * time.Second)
	}
}

func waitForDeployment(t *testing.T, c client.Client, name string) {
	t.Helper()

	eachPoll(t, readyTimeout, func() error {
		var d appsv1.Deployment
		key := types.NamespacedName{Namespace: namespace, Name: name}
		if err := c.Get(context.Background(), key, &d); err != nil {
			return err
		}

		desired := int32(1)
		if d.Spec.Replicas != nil {
			desired = *d.Spec.Replicas
		}
		ready := d.Status.ReadyReplicas
		// A Deployment's Available condition can hold true at zero desired replicas,
		// so readiness is judged from the replica counts, not that condition.
		if ready >= 1 && ready == desired {
			return nil
		}
		return fmt.Errorf("deployment %s not ready: %d/%d replicas ready", name, ready, desired)
	})
}

func TestControllerAndFakeBecomeAvailable(t *testing.T) {
	c := newClient(t)
	waitForDeployment(t, c, deploymentName)
	waitForDeployment(t, c, fakeDeployment)
}

func TestControllerPodIsReadyWithoutRestarts(t *testing.T) {
	c := newClient(t)
	ctx := context.Background()

	var pod corev1.Pod
	eachPoll(t, readyTimeout, func() error {
		var pods corev1.PodList
		if err := c.List(ctx, &pods,
			client.InNamespace(namespace),
			client.MatchingLabels{"app.kubernetes.io/name": "pangolin-gateway"},
		); err != nil {
			return fmt.Errorf("listing controller pods: %w", err)
		}
		if len(pods.Items) != 1 {
			return fmt.Errorf("got %d controller pods, want 1", len(pods.Items))
		}
		for _, cs := range pods.Items[0].Status.ContainerStatuses {
			if !cs.Ready {
				return fmt.Errorf("container %s is not ready", cs.Name)
			}
		}
		pod = pods.Items[0]
		return nil
	})

	// Restart count is judged once, after the pod is found ready: a restart that
	// already happened must fail the test, not be retried away.
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.RestartCount != 0 {
			t.Errorf("container %s restarted %d times; probes or configuration are wrong",
				cs.Name, cs.RestartCount)
		}
	}
}

// fakeState is what the fake reports through its control endpoint, which is how
// e2e inspects it: the test runs outside the cluster, the fake inside it.
type fakeState struct {
	Resources        map[string]map[string]any `json:"resources"`
	PrivateResources map[string]map[string]any `json:"privateResources"`
	Applies          int                       `json:"applies"`
	Deleted          []int                     `json:"deleted"`
	PrivateDeleted   []int                     `json:"privateDeleted"`
}

func readFakeState(t *testing.T) fakeState {
	t.Helper()

	raw := readThroughAPIServerProxy(t)

	var env struct {
		Data fakeState `json:"data"`
	}
	if err := json.Unmarshal([]byte(raw), &env); err != nil {
		t.Fatalf("decoding fake state %q: %v", raw, err)
	}
	return env.Data
}

// readThroughAPIServerProxy reaches the fake's ClusterIP Service through the API
// server's service proxy, so e2e needs neither an ingress nor a port-forward.
func readThroughAPIServerProxy(t *testing.T) string {
	t.Helper()

	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		clientcmd.NewDefaultClientConfigLoadingRules(),
		&clientcmd.ConfigOverrides{CurrentContext: kubeContext},
	).ClientConfig()
	if err != nil {
		t.Fatalf("building rest config: %v", err)
	}

	proxy := fmt.Sprintf(
		"%s/api/v1/namespaces/%s/services/pangolin-fake:http/proxy/_control/state",
		cfg.Host, namespace)

	transport, err := rest.TransportFor(cfg)
	if err != nil {
		t.Fatalf("building transport: %v", err)
	}
	req, err := http.NewRequest(http.MethodGet, proxy, nil)
	if err != nil {
		t.Fatalf("building request: %v", err)
	}

	resp, err := (&http.Client{Transport: transport, Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("reaching the fake through the API server proxy: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading fake response: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("fake proxy returned %d: %s", resp.StatusCode, body)
	}
	return string(body)
}

func TestRouteIsPublishedAndPruned(t *testing.T) {
	c := newClient(t)
	ctx := context.Background()

	waitForDeployment(t, c, deploymentName)
	waitForDeployment(t, c, fakeDeployment)

	gw := &gatewayv1.Gateway{}
	gw.Namespace = namespace
	gw.Name = "e2e-gw"
	gw.Spec.GatewayClassName = "pangolin"
	gw.Spec.Listeners = []gatewayv1.Listener{{
		Name: "http", Port: 80, Protocol: gatewayv1.HTTPProtocolType,
	}}
	if err := c.Create(ctx, gw); err != nil {
		t.Fatalf("creating Gateway: %v", err)
	}
	t.Cleanup(func() { _ = c.Delete(context.Background(), gw) })

	port := gatewayv1.PortNumber(8080)
	route := &gatewayv1.HTTPRoute{}
	route.Namespace = namespace
	route.Name = "e2e-web"
	route.Spec.ParentRefs = []gatewayv1.ParentReference{{Name: "e2e-gw"}}
	route.Spec.Hostnames = []gatewayv1.Hostname{"e2e.example.com"}
	route.Spec.Rules = []gatewayv1.HTTPRouteRule{{
		BackendRefs: []gatewayv1.HTTPBackendRef{{
			BackendRef: gatewayv1.BackendRef{
				BackendObjectReference: gatewayv1.BackendObjectReference{
					Name: "e2e-web", Port: &port,
				},
			},
		}},
	}}
	if err := c.Create(ctx, route); err != nil {
		t.Fatalf("creating HTTPRoute: %v", err)
	}
	// The test deletes this itself to exercise the prune; the cleanup is for the
	// runs that fail before getting there, so the next one starts clean.
	t.Cleanup(func() { _ = c.Delete(context.Background(), route) })

	wantKey := "gw-" + namespace + "-e2e-web"

	eachPoll(t, readyTimeout, func() error {
		state := readFakeState(t)
		entry, ok := state.Resources[wantKey]
		if !ok {
			return fmt.Errorf("%s not published yet; fake holds %v", wantKey, keysOf(state.Resources))
		}
		if got := entry["full-domain"]; got != "e2e.example.com" {
			return fmt.Errorf("full-domain = %v, want e2e.example.com", got)
		}
		return nil
	})

	// Status must reach the route, or ArgoCD reports it as progressing forever.
	eachPoll(t, readyTimeout, func() error {
		var r gatewayv1.HTTPRoute
		key := types.NamespacedName{Namespace: namespace, Name: "e2e-web"}
		if err := c.Get(ctx, key, &r); err != nil {
			return err
		}
		for _, ps := range r.Status.Parents {
			if string(ps.ControllerName) != "p3l1.de/pangolin" {
				continue
			}
			for _, cond := range ps.Conditions {
				if cond.Type == string(gatewayv1.RouteConditionAccepted) &&
					string(cond.Status) == "True" {
					return nil
				}
			}
		}
		return fmt.Errorf("no Accepted=True from our controller; parents = %+v", r.Status.Parents)
	})

	// Annotations do not bump generation, so an annotation-only edit reaches the
	// workqueue solely through AnnotationChangedPredicate. Without it this change
	// would sit unpublished until the next resync.
	eachPoll(t, readyTimeout, func() error {
		var r gatewayv1.HTTPRoute
		key := types.NamespacedName{Namespace: namespace, Name: "e2e-web"}
		if err := c.Get(ctx, key, &r); err != nil {
			return err
		}
		if r.Annotations == nil {
			r.Annotations = map[string]string{}
		}
		r.Annotations["pangolin.p3l1.de/access-rules"] =
			"- action: allow\n  match: path\n  value: /script.js\n"
		return c.Update(ctx, &r)
	})

	eachPoll(t, readyTimeout, func() error {
		entry, ok := readFakeState(t).Resources[wantKey]
		if !ok {
			return fmt.Errorf("%s vanished from the fake", wantKey)
		}
		rules, ok := entry["rules"].([]any)
		if !ok || len(rules) != 1 {
			return fmt.Errorf("rules = %v, want the annotated rule", entry["rules"])
		}
		if got := rules[0].(map[string]any)["value"]; got != "/script.js" {
			return fmt.Errorf("rules[0].value = %v, want /script.js", got)
		}
		return nil
	})

	if err := c.Delete(ctx, route); err != nil {
		t.Fatalf("deleting HTTPRoute: %v", err)
	}

	eachPoll(t, readyTimeout, func() error {
		state := readFakeState(t)
		if _, ok := state.Resources[wantKey]; ok {
			return fmt.Errorf("%s survived the route's deletion", wantKey)
		}
		if len(state.Deleted) == 0 {
			return fmt.Errorf("no DELETE reached the fake")
		}
		return nil
	})
}

func keysOf(m map[string]map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// Private resources are a second listing over a second table with its own
// delete route, so nothing about them is exercised by the public path.
func TestPrivateRouteIsPublishedAndPruned(t *testing.T) {
	c := newClient(t)
	ctx := context.Background()

	waitForDeployment(t, c, deploymentName)
	waitForDeployment(t, c, fakeDeployment)

	gw := &gatewayv1.Gateway{}
	gw.Namespace = namespace
	gw.Name = "e2e-private-gw"
	gw.Spec.GatewayClassName = "pangolin"
	gw.Spec.Listeners = []gatewayv1.Listener{{
		Name: "http", Port: 80, Protocol: gatewayv1.HTTPProtocolType,
	}}
	if err := c.Create(ctx, gw); err != nil {
		t.Fatalf("creating Gateway: %v", err)
	}
	t.Cleanup(func() { _ = c.Delete(context.Background(), gw) })

	port := gatewayv1.PortNumber(8080)
	route := &gatewayv1.HTTPRoute{}
	route.Namespace = namespace
	route.Name = "e2e-internal"
	route.Annotations = map[string]string{
		"pangolin.p3l1.de/visibility": "private",
		"pangolin.p3l1.de/roles":      "Member",
	}
	route.Spec.ParentRefs = []gatewayv1.ParentReference{{Name: "e2e-private-gw"}}
	route.Spec.Hostnames = []gatewayv1.Hostname{"e2e-internal.example.com"}
	route.Spec.Rules = []gatewayv1.HTTPRouteRule{{
		BackendRefs: []gatewayv1.HTTPBackendRef{{
			BackendRef: gatewayv1.BackendRef{
				BackendObjectReference: gatewayv1.BackendObjectReference{
					Name: "e2e-internal", Port: &port,
				},
			},
		}},
	}}
	if err := c.Create(ctx, route); err != nil {
		t.Fatalf("creating HTTPRoute: %v", err)
	}
	t.Cleanup(func() { _ = c.Delete(context.Background(), route) })

	wantKey := "gw-" + namespace + "-e2e-internal"

	eachPoll(t, readyTimeout, func() error {
		state := readFakeState(t)
		if _, ok := state.Resources[wantKey]; ok {
			return fmt.Errorf("%s reached the public section", wantKey)
		}
		entry, ok := state.PrivateResources[wantKey]
		if !ok {
			return fmt.Errorf("%s not published yet; fake holds %v",
				wantKey, keysOf(state.PrivateResources))
		}
		if got := entry["destination"]; got != "e2e-internal."+namespace+".svc.cluster.local" {
			return fmt.Errorf("destination = %v, want the Service FQDN", got)
		}
		if got := entry["mode"]; got != "http" {
			return fmt.Errorf("mode = %v, want http", got)
		}
		return nil
	})

	before := len(readFakeState(t).PrivateDeleted)

	if err := c.Delete(ctx, route); err != nil {
		t.Fatalf("deleting HTTPRoute: %v", err)
	}

	eachPoll(t, readyTimeout, func() error {
		state := readFakeState(t)
		if _, ok := state.PrivateResources[wantKey]; ok {
			return fmt.Errorf("%s survived the route's deletion", wantKey)
		}
		// Through the private endpoint, not the public one: the two sections
		// number their resources independently.
		if len(state.PrivateDeleted) <= before {
			return fmt.Errorf("no DELETE reached the private endpoint")
		}
		return nil
	})
}

// The credential is written by the controller's own ServiceAccount, so this is
// the only tier that proves the chart's `get` and `create` on Secrets suffice.
func TestBasicAuthCredentialIsGeneratedInTheCluster(t *testing.T) {
	c := newClient(t)
	ctx := context.Background()

	waitForDeployment(t, c, deploymentName)
	waitForDeployment(t, c, fakeDeployment)

	gw := &gatewayv1.Gateway{}
	gw.Namespace = namespace
	gw.Name = "e2e-auth-gw"
	gw.Spec.GatewayClassName = "pangolin"
	gw.Spec.Listeners = []gatewayv1.Listener{{
		Name: "http", Port: 80, Protocol: gatewayv1.HTTPProtocolType,
	}}
	if err := c.Create(ctx, gw); err != nil {
		t.Fatalf("creating Gateway: %v", err)
	}
	t.Cleanup(func() { _ = c.Delete(context.Background(), gw) })

	port := gatewayv1.PortNumber(8080)
	route := &gatewayv1.HTTPRoute{}
	route.Namespace = namespace
	route.Name = "e2e-auth"
	route.Annotations = map[string]string{
		gateway.AnnotationBasicAuth: "true",
		gateway.AnnotationSSO:       "false",
	}
	route.Spec.ParentRefs = []gatewayv1.ParentReference{{Name: "e2e-auth-gw"}}
	route.Spec.Hostnames = []gatewayv1.Hostname{"e2e-auth.example.com"}
	route.Spec.Rules = []gatewayv1.HTTPRouteRule{{
		BackendRefs: []gatewayv1.HTTPBackendRef{{
			BackendRef: gatewayv1.BackendRef{
				BackendObjectReference: gatewayv1.BackendObjectReference{
					Name: "e2e-auth", Port: &port,
				},
			},
		}},
	}}
	if err := c.Create(ctx, route); err != nil {
		t.Fatalf("creating HTTPRoute: %v", err)
	}
	t.Cleanup(func() { _ = c.Delete(context.Background(), route) })

	var password string
	eachPoll(t, readyTimeout, func() error {
		var secret corev1.Secret
		key := types.NamespacedName{
			Namespace: namespace,
			Name:      gateway.BasicAuthSecretName("e2e-auth"),
		}
		if err := c.Get(ctx, key, &secret); err != nil {
			return err
		}
		password = string(secret.Data["password"])
		if password == "" {
			return fmt.Errorf("secret %s carries no password", key)
		}
		return nil
	})

	eachPoll(t, readyTimeout, func() error {
		state := readFakeState(t)
		entry, ok := state.Resources["gw-"+namespace+"-e2e-auth"]
		if !ok {
			return fmt.Errorf("route not published yet; fake holds %v", keysOf(state.Resources))
		}
		auth, ok := entry["auth"].(map[string]any)
		if !ok {
			return fmt.Errorf("no auth block in %v", entry)
		}
		basic, ok := auth["basic-auth"].(map[string]any)
		if !ok {
			return fmt.Errorf("no basic-auth block in %v", auth)
		}
		if got := basic["password"]; got != password {
			return fmt.Errorf("published password = %v, want the Secret's", got)
		}
		if got := basic["extendedCompatibility"]; got != true {
			return fmt.Errorf("extendedCompatibility = %v, want true", got)
		}
		return nil
	})
}
