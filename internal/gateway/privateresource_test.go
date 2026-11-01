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

var created = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func privateResourceObj(namespace, name string, spec v1alpha1.PrivateResourceSpec) v1alpha1.PrivateResource {
	return v1alpha1.PrivateResource{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:         namespace,
			Name:              name,
			Generation:        3,
			CreationTimestamp: metav1.NewTime(created),
		},
		Spec: spec,
	}
}

// The four private resources the homelab blueprint maintains by hand today.
func tektonWebhook() v1alpha1.PrivateResource {
	return privateResourceObj("tekton-pipelines", "tekton-webhook", v1alpha1.PrivateResourceSpec{
		DisplayName: "Tekton Webhook",
		Mode:        v1alpha1.ModeHost,
		Sites:       []string{"intent-anniella-pulchra"},
		Destination: "el-pangolin-blueprints.tekton-pipelines.svc.cluster.local",
		TCPPorts:    "8080",
		Alias:       "tekton-webhook.homelab.internal",
		Machines:    []string{"fine-mastigoproctus-giganteus"},
	})
}

func mcpDocuments() v1alpha1.PrivateResource {
	return privateResourceObj("newt", "mcp-documents", v1alpha1.PrivateResourceSpec{
		DisplayName:     "Paperless MCP",
		Mode:            v1alpha1.ModeHTTP,
		Sites:           []string{"intent-anniella-pulchra"},
		FullDomain:      "mcp-documents.cloud.p3l1.de",
		SSL:             ptr(true),
		Scheme:          v1alpha1.SchemeHTTP,
		Destination:     "newt-paperless-mcp-service.newt.svc.cluster.local",
		DestinationPort: 3000,
		Roles:           []string{"Member"},
	})
}

func homeAssistant() v1alpha1.PrivateResource {
	return privateResourceObj("newt", "home-assistant", v1alpha1.PrivateResourceSpec{
		DisplayName:     "Home Assistant",
		Mode:            v1alpha1.ModeHTTP,
		Sites:           []string{"intent-anniella-pulchra"},
		FullDomain:      "home.cloud.p3l1.de",
		SSL:             ptr(true),
		Scheme:          v1alpha1.SchemeHTTP,
		Destination:     "newt-home-assistant-service.newt.svc.cluster.local",
		DestinationPort: 8123,
		Roles:           []string{"Member"},
	})
}

func arcaneManager() v1alpha1.PrivateResource {
	return privateResourceObj("arcane", "arcane-manager", v1alpha1.PrivateResourceSpec{
		DisplayName: "Arcane Manager",
		Mode:        v1alpha1.ModeHost,
		Sites:       []string{"intent-anniella-pulchra"},
		Destination: "arcane.arcane.svc.cluster.local",
		TCPPorts:    "3552",
		Alias:       "arcane-manager.homelab.internal",
		Machines:    []string{"pitiful-brongersmas-worm-snake"},
	})
}

func resourceInputs(resources ...v1alpha1.PrivateResource) Inputs {
	in := defaultInputs()
	in.PrivateResources = resources
	return in
}

func resourceVerdictFor(t *testing.T, r Result, ns, name string) ResourceVerdict {
	t.Helper()

	v, ok := r.PrivateResources[types.NamespacedName{Namespace: ns, Name: name}]
	if !ok {
		t.Fatalf("no verdict for PrivateResource %s/%s; have %v", ns, name, r.PrivateResources)
	}
	return v
}

// rejectedResource renders one PrivateResource and returns its Accepted
// condition, failing the test if it was published.
func rejectedResource(t *testing.T, in Inputs) ConditionResult {
	t.Helper()

	r := RenderAll(in)
	if len(r.Blueprint.PrivateResources) != 0 {
		t.Fatalf("published %v, want the resource rejected", r.Blueprint.PrivateResources)
	}
	pr := in.PrivateResources[0]
	v := resourceVerdictFor(t, r, pr.Namespace, pr.Name)
	if v.Published() {
		t.Errorf("verdict carries key %q, want none", v.Key)
	}
	if v.Accepted.Status != metav1.ConditionFalse {
		t.Errorf("Accepted = %+v, want False", v.Accepted)
	}
	return v.Accepted
}

func TestRenderPublishesTheHomelabPrivateResources(t *testing.T) {
	in := resourceInputs(tektonWebhook(), mcpDocuments(), homeAssistant(), arcaneManager())
	r := RenderAll(in)

	want := map[string]pangolin.PrivateResource{
		"gw-pr-tekton-pipelines-tekton-webhook": {
			Name:        "Tekton Webhook",
			Mode:        "host",
			Sites:       []string{"intent-anniella-pulchra"},
			Destination: "el-pangolin-blueprints.tekton-pipelines.svc.cluster.local",
			Alias:       "tekton-webhook.homelab.internal",
			TCPPorts:    ptr("8080"),
			UDPPorts:    ptr(""),
			DisableICMP: ptr(false),
			Machines:    []string{"fine-mastigoproctus-giganteus"},
		},
		"gw-pr-newt-mcp-documents": {
			Name:            "Paperless MCP",
			Mode:            "http",
			Sites:           []string{"intent-anniella-pulchra"},
			Destination:     "newt-paperless-mcp-service.newt.svc.cluster.local",
			DestinationPort: 3000,
			FullDomain:      "mcp-documents.cloud.p3l1.de",
			Scheme:          "http",
			SSL:             ptr(true),
			Roles:           []string{"Member"},
		},
		"gw-pr-newt-home-assistant": {
			Name:            "Home Assistant",
			Mode:            "http",
			Sites:           []string{"intent-anniella-pulchra"},
			Destination:     "newt-home-assistant-service.newt.svc.cluster.local",
			DestinationPort: 8123,
			FullDomain:      "home.cloud.p3l1.de",
			Scheme:          "http",
			SSL:             ptr(true),
			Roles:           []string{"Member"},
		},
		"gw-pr-arcane-arcane-manager": {
			Name:        "Arcane Manager",
			Mode:        "host",
			Sites:       []string{"intent-anniella-pulchra"},
			Destination: "arcane.arcane.svc.cluster.local",
			Alias:       "arcane-manager.homelab.internal",
			TCPPorts:    ptr("3552"),
			UDPPorts:    ptr(""),
			DisableICMP: ptr(false),
			Machines:    []string{"pitiful-brongersmas-worm-snake"},
		},
	}
	if !reflect.DeepEqual(r.Blueprint.PrivateResources, want) {
		t.Errorf("private-resources =\n%+v\nwant\n%+v", r.Blueprint.PrivateResources, want)
	}

	for _, pr := range in.PrivateResources {
		v := resourceVerdictFor(t, r, pr.Namespace, pr.Name)
		if v.Accepted.Status != metav1.ConditionTrue || v.Accepted.Reason != ReasonAccepted {
			t.Errorf("%s: Accepted = %+v, want True/%s", pr.Name, v.Accepted, ReasonAccepted)
		}
		if v.Generation != 3 {
			t.Errorf("%s: generation = %d, want 3", pr.Name, v.Generation)
		}
	}
}

func TestRenderPublishesACIDRResource(t *testing.T) {
	r := RenderAll(resourceInputs(privateResourceObj("infra", "lan", v1alpha1.PrivateResourceSpec{
		Mode:        v1alpha1.ModeCIDR,
		Destination: "192.168.10.0/24",
		TCPPorts:    "*",
		UDPPorts:    "53",
		DisableICMP: true,
	})))

	got, ok := r.Blueprint.PrivateResources["gw-pr-infra-lan"]
	if !ok {
		t.Fatalf("not published; have %v", r.Blueprint.PrivateResources)
	}
	want := pangolin.PrivateResource{
		Name:        "infra/lan",
		Mode:        "cidr",
		Sites:       []string{"default-site"},
		Destination: "192.168.10.0/24",
		TCPPorts:    ptr("*"),
		UDPPorts:    ptr("53"),
		DisableICMP: ptr(true),
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("entry = %+v, want %+v", got, want)
	}
}

// Pangolin's own defaults would differ: an absent tcp-ports means every port.
func TestRenderClosesPortsAHostResourceDoesNotName(t *testing.T) {
	r := RenderAll(resourceInputs(privateResourceObj("infra", "nas", v1alpha1.PrivateResourceSpec{
		Mode:        v1alpha1.ModeHost,
		Destination: "10.0.0.5",
	})))

	got := r.Blueprint.PrivateResources["gw-pr-infra-nas"]
	if got.TCPPorts == nil || *got.TCPPorts != "" || got.UDPPorts == nil || *got.UDPPorts != "" {
		t.Errorf("tcp-ports = %v, udp-ports = %v, want both sent as empty", got.TCPPorts, got.UDPPorts)
	}
	if got.Alias != "" {
		t.Errorf("alias = %q, want none: an IP destination needs no alias", got.Alias)
	}
}

func TestRenderDefaultsAnHTTPResource(t *testing.T) {
	r := RenderAll(resourceInputs(privateResourceObj("demo", "wiki", v1alpha1.PrivateResourceSpec{
		Mode:            v1alpha1.ModeHTTP,
		FullDomain:      "wiki.example.com",
		Destination:     "wiki.demo.svc.cluster.local",
		DestinationPort: 80,
	})))

	got := r.Blueprint.PrivateResources["gw-pr-demo-wiki"]
	if got.Name != "demo/wiki" {
		t.Errorf("name = %q, want the namespace/name fallback", got.Name)
	}
	if got.Scheme != "http" || got.SSL == nil || !*got.SSL {
		t.Errorf("scheme = %q, ssl = %v, want http and true", got.Scheme, got.SSL)
	}
	if got.TCPPorts != nil || got.UDPPorts != nil || got.DisableICMP != nil {
		t.Error("an http resource sent port or ICMP settings, which Pangolin forces")
	}
	if !equalStrings(got.Sites, []string{"default-site"}) {
		t.Errorf("sites = %v, want the default site", got.Sites)
	}
}

func TestRenderTurnsSSLOffWhenAsked(t *testing.T) {
	pr := mcpDocuments()
	pr.Spec.SSL = ptr(false)
	pr.Spec.Scheme = v1alpha1.SchemeHTTPS

	got := RenderAll(resourceInputs(pr)).Blueprint.PrivateResources["gw-pr-newt-mcp-documents"]
	if got.SSL == nil || *got.SSL {
		t.Errorf("ssl = %v, want false sent explicitly", got.SSL)
	}
	if got.Scheme != "https" {
		t.Errorf("scheme = %q, want https", got.Scheme)
	}
}

func TestRenderRejectsAnInvalidPrivateResource(t *testing.T) {
	host := func(mut func(*v1alpha1.PrivateResourceSpec)) v1alpha1.PrivateResource {
		pr := tektonWebhook()
		mut(&pr.Spec)
		return pr
	}
	http := func(mut func(*v1alpha1.PrivateResourceSpec)) v1alpha1.PrivateResource {
		pr := mcpDocuments()
		mut(&pr.Spec)
		return pr
	}
	cidr := func(mut func(*v1alpha1.PrivateResourceSpec)) v1alpha1.PrivateResource {
		pr := privateResourceObj("infra", "lan", v1alpha1.PrivateResourceSpec{
			Mode: v1alpha1.ModeCIDR, Destination: "10.0.0.0/8",
		})
		mut(&pr.Spec)
		return pr
	}

	for name, tc := range map[string]struct {
		resource v1alpha1.PrivateResource
		want     string
	}{
		"host without alias": {host(func(s *v1alpha1.PrivateResourceSpec) { s.Alias = "" }),
			"alias is required"},
		"host with an alias that is no FQDN": {host(func(s *v1alpha1.PrivateResourceSpec) { s.Alias = "tekton" }),
			"not a fully qualified domain name"},
		"wildcard alias before a hostname": {host(func(s *v1alpha1.PrivateResourceSpec) { s.Alias = "*.tekton.internal" }),
			"must not contain wildcards"},
		"host with a URL as destination": {host(func(s *v1alpha1.PrivateResourceSpec) { s.Destination = "http://tekton" }),
			"neither an IP address nor a hostname"},
		"host with fullDomain": {host(func(s *v1alpha1.PrivateResourceSpec) { s.FullDomain = "x.example.com" }),
			"fullDomain has no meaning in mode host"},
		"host with destinationPort": {host(func(s *v1alpha1.PrivateResourceSpec) { s.DestinationPort = 80 }),
			"destinationPort has no meaning in mode host"},
		"port out of range": {host(func(s *v1alpha1.PrivateResourceSpec) { s.TCPPorts = "70000" }),
			"tcpPorts"},
		"range backwards": {host(func(s *v1alpha1.PrivateResourceSpec) { s.UDPPorts = "9000-8000" }),
			"starts after it ends"},
		"empty port entry": {host(func(s *v1alpha1.PrivateResourceSpec) { s.TCPPorts = "80,,443" }),
			"empty entry"},
		"port not a number": {host(func(s *v1alpha1.PrivateResourceSpec) { s.TCPPorts = "http" }),
			"not a port"},
		"cidr with a host": {cidr(func(s *v1alpha1.PrivateResourceSpec) { s.Destination = "10.0.0.1" }),
			"not a CIDR block"},
		"cidr with alias": {cidr(func(s *v1alpha1.PrivateResourceSpec) { s.Alias = "lan.internal" }),
			"alias has no meaning in mode cidr"},
		"http without fullDomain": {http(func(s *v1alpha1.PrivateResourceSpec) { s.FullDomain = "" }),
			"fullDomain is required"},
		"http with a wildcard": {http(func(s *v1alpha1.PrivateResourceSpec) { s.FullDomain = "*.example.com" }),
			"no wildcard resources"},
		"http without destinationPort": {http(func(s *v1alpha1.PrivateResourceSpec) { s.DestinationPort = 0 }),
			"destinationPort is required"},
		"http with tcpPorts": {http(func(s *v1alpha1.PrivateResourceSpec) { s.TCPPorts = "80" }),
			"tcpPorts has no meaning in mode http"},
		"http with alias": {http(func(s *v1alpha1.PrivateResourceSpec) { s.Alias = "docs.internal" }),
			"alias has no meaning in mode http"},
		"http with disableIcmp": {http(func(s *v1alpha1.PrivateResourceSpec) { s.DisableICMP = true }),
			"disableIcmp has no meaning in mode http"},
		"the Admin role": {http(func(s *v1alpha1.PrivateResourceSpec) { s.Roles = []string{"Member", "Admin"} }),
			"must not name Admin"},
		"an empty role": {http(func(s *v1alpha1.PrivateResourceSpec) { s.Roles = []string{""} }),
			"roles has an empty entry"},
		"an empty machine": {host(func(s *v1alpha1.PrivateResourceSpec) { s.Machines = []string{" "} }),
			"machines has an empty entry"},
		"a blank display name": {host(func(s *v1alpha1.PrivateResourceSpec) { s.DisplayName = "  " }),
			"displayName is blank"},
		"an empty site": {host(func(s *v1alpha1.PrivateResourceSpec) { s.Sites = []string{""} }),
			"sites has an empty entry"},
	} {
		t.Run(name, func(t *testing.T) {
			got := rejectedResource(t, resourceInputs(tc.resource))
			if got.Reason != ReasonInvalidSpec {
				t.Errorf("reason = %q, want %q", got.Reason, ReasonInvalidSpec)
			}
			if !strings.Contains(got.Message, tc.want) {
				t.Errorf("message = %q, want it to mention %q", got.Message, tc.want)
			}
		})
	}
}

func TestRenderRejectsUnknownNamesBeforePangolinDoes(t *testing.T) {
	for name, tc := range map[string]struct {
		mutate func(*Inputs)
		reason string
		want   string
	}{
		"site": {func(in *Inputs) { in.KnownSites = map[string]bool{"other": true} },
			ReasonUnknownSite, `site "intent-anniella-pulchra"`},
		"machine": {func(in *Inputs) { in.KnownMachines = map[string]bool{"other": true} },
			ReasonUnknownMachine, `none of the machines exist in this organisation ("fine-mastigoproctus-giganteus")`},
	} {
		t.Run(name, func(t *testing.T) {
			in := resourceInputs(tektonWebhook())
			tc.mutate(&in)
			got := rejectedResource(t, in)
			if got.Reason != tc.reason || !strings.Contains(got.Message, tc.want) {
				t.Errorf("Accepted = %+v, want reason %s mentioning %s", got, tc.reason, tc.want)
			}
		})
	}

	t.Run("user", func(t *testing.T) {
		pr := mcpDocuments()
		pr.Spec.Users = []string{"ghost", "bob@example.com"}
		in := resourceInputs(pr)
		in.KnownUsers = map[string]bool{"alice": true}
		got := rejectedResource(t, in)
		if got.Reason != ReasonUnknownUser || !strings.Contains(got.Message, "bob@example.com") {
			t.Errorf("Accepted = %+v, want %s naming bob@example.com", got, ReasonUnknownUser)
		}
	})
}

// Pangolin drops a name it cannot find while another resolves. Rejecting the
// resource for it would unpublish it for everyone the list still grants.
func TestRenderPublishesGrantsOfWhichSomeResolveWithANote(t *testing.T) {
	pr := tektonWebhook()
	pr.Spec.Machines = []string{"fine-mastigoproctus-giganteus", "departed-runner"}
	pr.Spec.Users = []string{"alice", "bob@example.com"}
	in := resourceInputs(pr)
	in.KnownMachines = map[string]bool{"fine-mastigoproctus-giganteus": true}
	in.KnownUsers = map[string]bool{"alice": true}

	v := resourceVerdictFor(t, RenderAll(in), pr.Namespace, pr.Name)
	if !v.Published() || v.Accepted.Status != metav1.ConditionTrue {
		t.Fatalf("verdict = %+v, want published", v)
	}
	for _, want := range []string{`users "bob@example.com" do not exist`, `machines "departed-runner" do not exist`} {
		if !strings.Contains(v.Accepted.Message, want) {
			t.Errorf("message = %q, want it to say %q", v.Accepted.Message, want)
		}
	}
}

// A wildcard alias is fine in front of an IP; only a hostname needs a plain one.
func TestRenderAcceptsAWildcardAliasForAnIP(t *testing.T) {
	pr := privateResourceObj("infra", "nas", v1alpha1.PrivateResourceSpec{
		Mode: v1alpha1.ModeHost, Destination: "10.0.0.5", Alias: "nas-0?.homelab.internal",
	})
	if v := resourceVerdictFor(t, RenderAll(resourceInputs(pr)), "infra", "nas"); !v.Published() {
		t.Errorf("rejected: %+v", v.Accepted)
	}
}

// A listing that failed leaves its map nil, and the check then defers to Pangolin.
func TestRenderSkipsTheNameChecksWithoutAListing(t *testing.T) {
	pr := tektonWebhook()
	pr.Spec.Users = []string{"anyone"}
	r := RenderAll(resourceInputs(pr))

	if !resourceVerdictFor(t, r, pr.Namespace, pr.Name).Published() {
		t.Errorf("rejected without any listing to check against: %+v", r.PrivateResources)
	}
}

func TestRenderGivesARouteAndAResourceOfOneNameSeparateKeys(t *testing.T) {
	in := resourceInputs(privateResourceObj("demo", "web", v1alpha1.PrivateResourceSpec{
		Mode: v1alpha1.ModeHost, Destination: "10.0.0.1",
	}))
	in.Routes = []gatewayv1.HTTPRoute{route("demo", "web", private())}

	r := RenderAll(in)
	if _, ok := r.Blueprint.PrivateResources["gw-demo-web"]; !ok {
		t.Error("the route lost its key to the PrivateResource")
	}
	if _, ok := r.Blueprint.PrivateResources["gw-pr-demo-web"]; !ok {
		t.Error("the PrivateResource was not published under its own key")
	}
}

// Oldest wins whichever source is older, and the loser names the winner by kind.
func TestRenderResolvesAHostnameCollisionBetweenARouteAndAResource(t *testing.T) {
	for name, tc := range map[string]struct {
		routeAge, resourceAge time.Time
		wantWinner            string
		wantMessage           string
	}{
		"route older": {created, created.Add(time.Hour), "gw-demo-docs",
			"already claimed by HTTPRoute demo/docs"},
		"resource older": {created.Add(time.Hour), created, "gw-pr-demo-wiki",
			"already claimed by PrivateResource demo/wiki"},
	} {
		t.Run(name, func(t *testing.T) {
			pr := privateResourceObj("demo", "wiki", v1alpha1.PrivateResourceSpec{
				Mode: v1alpha1.ModeHTTP, FullDomain: "docs.example.com",
				Destination: "wiki.demo.svc.cluster.local", DestinationPort: 80,
			})
			pr.CreationTimestamp = metav1.NewTime(tc.resourceAge)
			in := resourceInputs(pr)
			in.Routes = []gatewayv1.HTTPRoute{
				route("demo", "docs", private(), withCreation(tc.routeAge)),
			}

			r := RenderAll(in)
			if len(r.Blueprint.PrivateResources) != 1 {
				t.Fatalf("published %v, want exactly the winner", r.Blueprint.PrivateResources)
			}
			if _, ok := r.Blueprint.PrivateResources[tc.wantWinner]; !ok {
				t.Errorf("published %v, want %s", r.Blueprint.PrivateResources, tc.wantWinner)
			}

			var loser ConditionResult
			if tc.wantWinner == "gw-demo-docs" {
				loser = resourceVerdictFor(t, r, "demo", "wiki").Accepted
			} else {
				loser = verdictFor(t, r.Routes, "demo", "docs").Accepted
			}
			if loser.Reason != ReasonDuplicateHostname || !strings.Contains(loser.Message, tc.wantMessage) {
				t.Errorf("loser Accepted = %+v, want %s saying %q", loser, ReasonDuplicateHostname, tc.wantMessage)
			}
		})
	}
}

// Pangolin checks full-domain per section, so a public route and a private
// resource may share a hostname; a connected client reaches the private one.
func TestRenderSharesAHostnameBetweenAPublicRouteAndAResource(t *testing.T) {
	pr := mcpDocuments()
	pr.CreationTimestamp = metav1.NewTime(created.Add(time.Hour))
	in := resourceInputs(pr)
	in.Routes = []gatewayv1.HTTPRoute{
		route("demo", "mcp", withHostnames("MCP-documents.cloud.p3l1.de"), withCreation(created)),
	}

	r := RenderAll(in)
	if v := verdictFor(t, r.Routes, "demo", "mcp"); !v.Published() {
		t.Errorf("public route verdict = %+v, want published", v.Accepted)
	}
	if v := resourceVerdictFor(t, r, "newt", "mcp-documents"); !v.Published() {
		t.Errorf("resource verdict = %+v, want published beside the public route", v.Accepted)
	}
	if _, ok := r.Blueprint.PublicResources["gw-demo-mcp"]; !ok {
		t.Error("the public route is missing from public-resources")
	}
	if _, ok := r.Blueprint.PrivateResources["gw-pr-newt-mcp-documents"]; !ok {
		t.Error("the resource is missing from private-resources")
	}
}

// An alias belongs to one site resource in the organisation; a second would
// fail the whole apply.
func TestRenderResolvesAnAliasCollision(t *testing.T) {
	older := tektonWebhook()
	newer := arcaneManager()
	newer.Spec.Alias = older.Spec.Alias
	newer.CreationTimestamp = metav1.NewTime(created.Add(time.Minute))

	r := RenderAll(resourceInputs(newer, older))
	if _, ok := r.Blueprint.PrivateResources["gw-pr-tekton-pipelines-tekton-webhook"]; !ok {
		t.Error("the older resource lost its alias")
	}
	v := resourceVerdictFor(t, r, "arcane", "arcane-manager")
	if v.Published() || v.Accepted.Reason != ReasonDuplicateHostname {
		t.Errorf("newer verdict = %+v, want rejected as %s", v, ReasonDuplicateHostname)
	}
}

func TestRenderResolvesAKeyCollisionBetweenResources(t *testing.T) {
	// "a-b"/"c" and "a"/"b-c" both make gw-pr-a-b-c.
	first := privateResourceObj("a-b", "c", v1alpha1.PrivateResourceSpec{
		Mode: v1alpha1.ModeHost, Destination: "10.0.0.1",
	})
	second := privateResourceObj("a", "b-c", v1alpha1.PrivateResourceSpec{
		Mode: v1alpha1.ModeHost, Destination: "10.0.0.2",
	})

	r := RenderAll(resourceInputs(first, second))
	if got := r.Blueprint.PrivateResources["gw-pr-a-b-c"].Destination; got != "10.0.0.2" {
		t.Errorf("destination = %q, want a/b-c's: equal ages fall back to namespace order", got)
	}
	if v := resourceVerdictFor(t, r, "a-b", "c"); v.Accepted.Reason != ReasonDuplicateKey {
		t.Errorf("loser Accepted = %+v, want %s", v.Accepted, ReasonDuplicateKey)
	}
}

func TestRenderIsDeterministicAcrossBothSources(t *testing.T) {
	in := resourceInputs(tektonWebhook(), mcpDocuments(), homeAssistant(), arcaneManager())
	in.Routes = []gatewayv1.HTTPRoute{
		route("demo", "home", private(), withHostnames("home.cloud.p3l1.de"), withCreation(created)),
	}
	first := RenderAll(in)
	for range 20 {
		if again := RenderAll(in); !reflect.DeepEqual(first, again) {
			t.Fatal("two renders of the same inputs differ")
		}
	}
}

// The Admin role and unknown users fail the whole document on the private
// side, whether a route or a PrivateResource names them.
func TestRenderAppliesThePrivateGrantChecksToRoutes(t *testing.T) {
	msg := rejectedRoute(t, private(), annotate(map[string]string{AnnotationRoles: "Admin"}))
	if !strings.Contains(msg, "Admin") {
		t.Errorf("message = %q, want it to name the Admin role", msg)
	}

	in := defaultInputs(route("demo", "web", private(),
		annotate(map[string]string{AnnotationUsers: "ghost"})))
	in.KnownUsers = map[string]bool{"alice": true}
	_, verdicts := Render(in)
	if v := verdictFor(t, verdicts, "demo", "web"); v.Published() ||
		!strings.Contains(v.Accepted.Message, `"ghost"`) {
		t.Errorf("verdict = %+v, want rejected naming the unknown user", v)
	}

	// One departed user among others must not unpublish the route.
	in = defaultInputs(route("demo", "web", private(),
		annotate(map[string]string{AnnotationUsers: "alice, ghost"})))
	in.KnownUsers = map[string]bool{"alice": true}
	_, verdicts = Render(in)
	if v := verdictFor(t, verdicts, "demo", "web"); !v.Published() ||
		!strings.Contains(v.Accepted.Message, `users "ghost" do not exist`) {
		t.Errorf("verdict = %+v, want published with a note on the unknown user", v)
	}
}
