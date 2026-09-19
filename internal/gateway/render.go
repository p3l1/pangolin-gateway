// SPDX-License-Identifier: AGPL-3.0-only

// Package gateway turns Gateway API objects into a Pangolin blueprint.
package gateway

import (
	"fmt"
	"sort"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/p3l1/pangolin-gateway/internal/pangolin"
)

// Annotations carrying what the Gateway API has no field for.
const (
	AnnotationSSO  = "pangolin.p3l1.de/sso"
	AnnotationSite = "pangolin.p3l1.de/site"
)

// KeyPrefix scopes everything this controller owns. The prune touches nothing
// without it, so a hand-written blueprint can maintain resources alongside.
const KeyPrefix = "gw-"

// Reasons beyond the Gateway API's own set. The spec allows implementation
// specific reasons; these two describe collisions only this controller can see.
const (
	ReasonDuplicateKey      = "DuplicateResourceKey"
	ReasonDuplicateHostname = "DuplicateHostname"
	ReasonUnknownSite       = "UnknownSite"
	ReasonPublishFailed     = "PublishFailed"
	ReasonPublished         = "Published"
)

type Inputs struct {
	Routes         []gatewayv1.HTTPRoute
	Gateways       []gatewayv1.Gateway
	GatewayClasses []gatewayv1.GatewayClass
	ControllerName string
	DefaultSite    string

	// KnownSites holds the site niceIds Pangolin accepts. Pangolin rejects the
	// whole apply for a single unknown site, so one mistyped annotation would
	// otherwise unpublish every route. A nil map disables the check, which is how
	// a controller whose key lacks the listSites action keeps working.
	KnownSites map[string]bool
}

// ConditionResult is one condition's outcome, without the bookkeeping fields
// that only the status writer can fill in.
type ConditionResult struct {
	Status  metav1.ConditionStatus
	Reason  string
	Message string
}

func accepted() ConditionResult {
	return ConditionResult{
		Status:  metav1.ConditionTrue,
		Reason:  string(gatewayv1.RouteReasonAccepted),
		Message: "Route published to Pangolin",
	}
}

func resolved() ConditionResult {
	return ConditionResult{
		Status:  metav1.ConditionTrue,
		Reason:  string(gatewayv1.RouteReasonResolvedRefs),
		Message: "Backend resolved",
	}
}

func rejected(reason gatewayv1.RouteConditionReason, format string, args ...any) ConditionResult {
	return ConditionResult{
		Status:  metav1.ConditionFalse,
		Reason:  string(reason),
		Message: fmt.Sprintf(format, args...),
	}
}

// Verdict is what the renderer concluded about one route this controller serves.
// ParentRefs lists only the served parents, because status is written per parent
// and another controller's entries must be left alone.
type Verdict struct {
	Key          string
	Generation   int64
	ParentRefs   []gatewayv1.ParentReference
	Accepted     ConditionResult
	ResolvedRefs ConditionResult
}

// Published reports whether this route made it into the blueprint.
func (v Verdict) Published() bool { return v.Key != "" }

// candidate is a route that passed validation and is competing for its key and
// hostname against the other candidates.
type candidate struct {
	name      types.NamespacedName
	key       string
	createdAt metav1.Time
	resource  pangolin.PublicResource
	verdict   Verdict
}

// Render turns the cluster's Gateway API objects into the blueprint Pangolin
// should hold, plus one verdict per route this controller serves. It is pure:
// the same inputs always produce the same blueprint, including on collisions.
func Render(in Inputs) (pangolin.Blueprint, map[types.NamespacedName]Verdict) {
	classes := servedClasses(in.GatewayClasses, in.ControllerName)
	gateways := gatewaysByName(in.Gateways)

	verdicts := make(map[types.NamespacedName]Verdict)
	var candidates []candidate

	for _, r := range in.Routes {
		served, anyParentFound := servedParents(r, gateways, classes)
		if len(served) == 0 {
			// Not ours: writing status here would clobber the owning controller's
			// entry. A parentRef that resolves to nothing at all is ours to report
			// only if no other controller could claim it either.
			if !anyParentFound && hasOnlyUnresolvableParents(r, gateways) {
				verdicts[nameOf(r)] = Verdict{
					Generation: r.Generation,
					ParentRefs: r.Spec.ParentRefs,
					Accepted: rejected(gatewayv1.RouteReasonNoMatchingParent,
						"no Gateway found for any parentRef"),
					ResolvedRefs: resolved(),
				}
			}
			continue
		}

		base := Verdict{
			Generation: r.Generation,
			ParentRefs: served,
		}

		resource, key, cond := translate(r, in.DefaultSite, in.KnownSites)
		if cond != nil {
			base.Accepted, base.ResolvedRefs = *cond, resolved()
			if isRefReason(cond.Reason) {
				base.Accepted = accepted()
				base.Accepted.Status = metav1.ConditionFalse
				base.Accepted.Reason = string(gatewayv1.RouteReasonUnsupportedValue)
				base.Accepted.Message = cond.Message
				base.ResolvedRefs = *cond
			}
			verdicts[nameOf(r)] = base
			continue
		}

		base.Accepted, base.ResolvedRefs = accepted(), resolved()
		candidates = append(candidates, candidate{
			name:      nameOf(r),
			key:       key,
			createdAt: r.CreationTimestamp,
			resource:  resource,
			verdict:   base,
		})
	}

	bp := pangolin.Blueprint{PublicResources: map[string]pangolin.PublicResource{}}
	resolveCollisions(candidates, bp, verdicts)
	return bp, verdicts
}

// resolveCollisions awards each key and hostname to its oldest claimant. Order is
// total — creation time, then namespace, then name — so equal timestamps cannot
// make the published set depend on map iteration order.
func resolveCollisions(
	candidates []candidate,
	bp pangolin.Blueprint,
	verdicts map[types.NamespacedName]Verdict,
) {
	sort.SliceStable(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		switch {
		case !a.createdAt.Equal(&b.createdAt):
			return a.createdAt.Before(&b.createdAt)
		case a.name.Namespace != b.name.Namespace:
			return a.name.Namespace < b.name.Namespace
		default:
			return a.name.Name < b.name.Name
		}
	})

	keyOwner := map[string]types.NamespacedName{}
	hostOwner := map[string]types.NamespacedName{}

	for _, c := range candidates {
		if owner, taken := keyOwner[c.key]; taken {
			c.verdict.Accepted = rejected(gatewayv1.RouteConditionReason(ReasonDuplicateKey),
				"resource key %q is already claimed by %s", c.key, owner)
			c.verdict.Key = ""
			verdicts[c.name] = c.verdict
			continue
		}
		if owner, taken := hostOwner[c.resource.FullDomain]; taken {
			c.verdict.Accepted = rejected(gatewayv1.RouteConditionReason(ReasonDuplicateHostname),
				"hostname %q is already claimed by %s", c.resource.FullDomain, owner)
			c.verdict.Key = ""
			verdicts[c.name] = c.verdict
			continue
		}

		keyOwner[c.key] = c.name
		hostOwner[c.resource.FullDomain] = c.name
		bp.PublicResources[c.key] = c.resource

		c.verdict.Key = c.key
		verdicts[c.name] = c.verdict
	}
}

// translate validates one route and builds its blueprint entry. A non-nil
// condition means the route is rejected and carries the reason why.
func translate(
	r gatewayv1.HTTPRoute,
	defaultSite string,
	knownSites map[string]bool,
) (pangolin.PublicResource, string, *ConditionResult) {
	switch {
	case len(r.Spec.Hostnames) == 0:
		return fail(gatewayv1.RouteReasonUnsupportedValue,
			"a hostname is required; v0.1 publishes one hostname per route")
	case len(r.Spec.Hostnames) > 1:
		return fail(gatewayv1.RouteReasonUnsupportedValue,
			"v0.1 publishes one hostname per route, found %d", len(r.Spec.Hostnames))
	}

	host := string(r.Spec.Hostnames[0])
	// Pangolin has no wildcard resources — wildcards apply to certificates only —
	// and a wildcard full-domain would fail the apply for every other route too.
	if strings.HasPrefix(host, "*") {
		return fail(gatewayv1.RouteReasonUnsupportedValue,
			"Pangolin has no wildcard resources; %q cannot be published", host)
	}

	if len(r.Spec.Rules) != 1 {
		return fail(gatewayv1.RouteReasonUnsupportedValue,
			"v0.1 publishes exactly one rule per route, found %d", len(r.Spec.Rules))
	}
	rule := r.Spec.Rules[0]

	// Publishing a route while dropping its filters would proxy the whole domain
	// to one backend, contradicting what the route says it does.
	if len(rule.Filters) > 0 {
		return fail(gatewayv1.RouteReasonUnsupportedValue,
			"v0.1 cannot represent route filters; found %d", len(rule.Filters))
	}
	if len(rule.BackendRefs) != 1 {
		return fail(gatewayv1.RouteReasonUnsupportedValue,
			"v0.1 publishes exactly one backendRef per route, found %d", len(rule.BackendRefs))
	}
	backend := rule.BackendRefs[0]

	if kind := backend.Kind; kind != nil && *kind != "Service" {
		return fail(gatewayv1.RouteReasonInvalidKind,
			"backendRef kind %q is not supported; only Service is", *kind)
	}
	if group := backend.Group; group != nil && *group != "" {
		return fail(gatewayv1.RouteReasonInvalidKind,
			"backendRef group %q is not supported; only the core group is", *group)
	}
	// A cross-namespace backendRef needs a ReferenceGrant to be honoured. Rather
	// than resolve grants, v0.1 refuses: an unchecked ref is a cross-tenant leak.
	if ns := backend.Namespace; ns != nil && string(*ns) != r.Namespace {
		return fail(gatewayv1.RouteReasonRefNotPermitted,
			"cross-namespace backendRef to %q needs a ReferenceGrant, which v0.1 does not resolve", *ns)
	}
	if backend.Port == nil {
		return fail(gatewayv1.RouteReasonUnsupportedValue, "backendRef has no port")
	}

	sso, err := ssoFor(r)
	if err != nil {
		return fail(gatewayv1.RouteReasonUnsupportedValue, "%s", err)
	}

	site := defaultSite
	if v, ok := r.Annotations[AnnotationSite]; ok && v != "" {
		site = v
	}
	if knownSites != nil && !knownSites[site] {
		return fail(gatewayv1.RouteConditionReason(ReasonUnknownSite),
			"site %q does not exist in this Pangolin organisation", site)
	}

	resource := pangolin.PublicResource{
		Name:       r.Namespace + "/" + r.Name,
		Mode:       pangolin.ModeHTTP,
		FullDomain: host,
		Auth:       &pangolin.Auth{SSOEnabled: sso},
		Targets: []pangolin.Target{{
			Site:     site,
			Method:   pangolin.MethodHTTP,
			Hostname: fmt.Sprintf("%s.%s.svc.cluster.local", backend.Name, r.Namespace),
			Port:     int32(*backend.Port),
		}},
	}
	return resource, Key(r.Namespace, r.Name), nil
}

func fail(
	reason gatewayv1.RouteConditionReason,
	format string,
	args ...any,
) (pangolin.PublicResource, string, *ConditionResult) {
	c := rejected(reason, format, args...)
	return pangolin.PublicResource{}, "", &c
}

// Key is the blueprint key for a route. Collisions are possible because hyphens
// are legal in both halves, so Render awards a contested key to its oldest
// claimant rather than letting two routes overwrite each other.
func Key(namespace, name string) string {
	return KeyPrefix + namespace + "-" + name
}

// ssoFor fails closed: an unreadable value must neither publish a service
// unprotected nor silently protect one the author meant to open.
func ssoFor(r gatewayv1.HTTPRoute) (bool, error) {
	v, ok := r.Annotations[AnnotationSSO]
	if !ok || v == "" {
		return true, nil
	}
	switch strings.ToLower(v) {
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, fmt.Errorf("annotation %s must be \"true\" or \"false\", got %q",
			AnnotationSSO, v)
	}
}

func isRefReason(reason string) bool {
	switch gatewayv1.RouteConditionReason(reason) {
	case gatewayv1.RouteReasonInvalidKind,
		gatewayv1.RouteReasonRefNotPermitted,
		gatewayv1.RouteReasonBackendNotFound:
		return true
	default:
		return false
	}
}

func nameOf(r gatewayv1.HTTPRoute) types.NamespacedName {
	return types.NamespacedName{Namespace: r.Namespace, Name: r.Name}
}

func servedClasses(classes []gatewayv1.GatewayClass, controllerName string) map[string]bool {
	served := make(map[string]bool, len(classes))
	for _, c := range classes {
		if string(c.Spec.ControllerName) == controllerName {
			served[c.Name] = true
		}
	}
	return served
}

func gatewaysByName(gateways []gatewayv1.Gateway) map[types.NamespacedName]gatewayv1.Gateway {
	byName := make(map[types.NamespacedName]gatewayv1.Gateway, len(gateways))
	for _, g := range gateways {
		byName[types.NamespacedName{Namespace: g.Namespace, Name: g.Name}] = g
	}
	return byName
}

// servedParents returns the parentRefs pointing at a Gateway of a class this
// controller serves, and whether any parentRef resolved to a Gateway at all.
func servedParents(
	r gatewayv1.HTTPRoute,
	gateways map[types.NamespacedName]gatewayv1.Gateway,
	classes map[string]bool,
) (served []gatewayv1.ParentReference, anyFound bool) {
	for _, ref := range r.Spec.ParentRefs {
		if !refersToGateway(ref) {
			continue
		}
		g, ok := gateways[parentKey(ref, r.Namespace)]
		if !ok {
			continue
		}
		anyFound = true
		if classes[string(g.Spec.GatewayClassName)] {
			served = append(served, ref)
		}
	}
	return served, anyFound
}

// hasOnlyUnresolvableParents reports whether every Gateway-kind parentRef names a
// Gateway that does not exist, which is the one case where a route none of whose
// parents resolve is still ours to report on.
func hasOnlyUnresolvableParents(
	r gatewayv1.HTTPRoute,
	gateways map[types.NamespacedName]gatewayv1.Gateway,
) bool {
	var gatewayRefs int
	for _, ref := range r.Spec.ParentRefs {
		if !refersToGateway(ref) {
			continue
		}
		gatewayRefs++
		if _, ok := gateways[parentKey(ref, r.Namespace)]; ok {
			return false
		}
	}
	return gatewayRefs > 0
}

func refersToGateway(ref gatewayv1.ParentReference) bool {
	if ref.Group != nil && *ref.Group != gatewayv1.GroupName && *ref.Group != "" {
		return false
	}
	return ref.Kind == nil || *ref.Kind == "Gateway"
}

func parentKey(ref gatewayv1.ParentReference, routeNamespace string) types.NamespacedName {
	ns := routeNamespace
	if ref.Namespace != nil {
		ns = string(*ref.Namespace)
	}
	return types.NamespacedName{Namespace: ns, Name: string(ref.Name)}
}
