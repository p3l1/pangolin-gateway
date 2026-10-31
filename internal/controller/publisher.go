// SPDX-License-Identifier: AGPL-3.0-only

// Package controller reconciles Gateway API objects into a Pangolin instance.
package controller

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/p3l1/pangolin-gateway/api/v1alpha1"
	"github.com/p3l1/pangolin-gateway/internal/gateway"
	"github.com/p3l1/pangolin-gateway/internal/pangolin"
)

// wholeState is the single request every watch maps to. One key means the
// workqueue coalesces a burst of events into one pass, which is what lets the
// controller send the entire desired state in a single apply.
var wholeState = reconcile.Request{
	NamespacedName: types.NamespacedName{Name: "whole-state"},
}

// DefaultResyncInterval repairs drift Pangolin-side. Pangolin is not watched, so
// a resource deleted or edited out of band is otherwise never repaired until the
// next cluster event, which may be days away.
const DefaultResyncInterval = 5 * time.Minute

type Publisher struct {
	Client         client.Client
	Pangolin       pangolin.Client
	ControllerName string
	DefaultSite    string
	ResyncInterval time.Duration

	// Licensed enables the Pangolin features that need a licence: rendering
	// ResourcePolicies and pruning the policies they leave behind.
	Licensed bool

	// DryRun must match the decorator wrapping Pangolin. It only affects status:
	// reporting a plain Accepted=True while nothing was published would make the
	// rehearsal look like a successful rollout.
	DryRun bool
}

// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=httproutes;gateways;gatewayclasses,verbs=get;list;watch
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=httproutes/status;gateways/status;gatewayclasses/status,verbs=get;patch;update
// +kubebuilder:rbac:groups=pangolin.p3l1.de,resources=privateresources,verbs=get;list;watch
// +kubebuilder:rbac:groups=pangolin.p3l1.de,resources=privateresources/status,verbs=get;patch;update
// +kubebuilder:rbac:groups=pangolin.p3l1.de,resources=resourcepolicies,verbs=get;list;watch
// +kubebuilder:rbac:groups=pangolin.p3l1.de,resources=resourcepolicies/status,verbs=get;patch;update
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

func (p *Publisher) SetupWithManager(mgr ctrl.Manager) error {
	// Status writes are themselves watch events. Generation does not move on a
	// status-only update, so this predicate keeps the controller from looping
	// against itself — OR'd with annotations, which this controller reads but
	// which do not bump generation either.
	specOrAnnotations := builder.WithPredicates(predicate.Or(
		predicate.GenerationChangedPredicate{},
		predicate.AnnotationChangedPredicate{},
	))

	toWholeState := handler.EnqueueRequestsFromMapFunc(
		func(context.Context, client.Object) []reconcile.Request {
			return []reconcile.Request{wholeState}
		})

	return ctrl.NewControllerManagedBy(mgr).
		Named("pangolin-publisher").
		Watches(&gatewayv1.HTTPRoute{}, toWholeState, specOrAnnotations).
		Watches(&gatewayv1.Gateway{}, toWholeState, specOrAnnotations).
		Watches(&gatewayv1.GatewayClass{}, toWholeState, specOrAnnotations).
		Watches(&v1alpha1.PrivateResource{}, toWholeState, specOrAnnotations).
		Watches(&v1alpha1.ResourcePolicy{}, toWholeState, specOrAnnotations).
		Complete(p)
}

func (p *Publisher) Reconcile(ctx context.Context, _ ctrl.Request) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)

	// Every read must succeed before anything is written. A partial view would
	// render a partial desired state, and the prune deletes on that result.
	var (
		classes   gatewayv1.GatewayClassList
		gateways  gatewayv1.GatewayList
		routes    gatewayv1.HTTPRouteList
		resources v1alpha1.PrivateResourceList
		policies  v1alpha1.ResourcePolicyList
	)
	if err := p.Client.List(ctx, &classes); err != nil {
		return ctrl.Result{}, fmt.Errorf("listing GatewayClasses: %w", err)
	}
	if err := p.Client.List(ctx, &gateways); err != nil {
		return ctrl.Result{}, fmt.Errorf("listing Gateways: %w", err)
	}
	if err := p.Client.List(ctx, &routes); err != nil {
		return ctrl.Result{}, fmt.Errorf("listing HTTPRoutes: %w", err)
	}
	if err := p.Client.List(ctx, &resources); err != nil {
		return ctrl.Result{}, fmt.Errorf("listing PrivateResources: %w", err)
	}
	if err := p.Client.List(ctx, &policies); err != nil {
		return ctrl.Result{}, fmt.Errorf("listing ResourcePolicies: %w", err)
	}

	// Pangolin rejects the entire apply for one unknown site, so the renderer
	// checks names up front. If the listing is unavailable — typically a key
	// without the listSites action — the check is skipped rather than failing
	// every route, and Pangolin stays the authority it already was.
	var knownSites map[string]bool
	sites, err := p.Pangolin.ListSites(ctx)
	if err != nil {
		log.Error(err, "listing sites; site names will not be checked before the apply")
	} else {
		knownSites = make(map[string]bool, len(sites))
		for _, s := range sites {
			knownSites[s.NiceID] = true
		}
	}

	inputs := gateway.Inputs{
		Routes:           routes.Items,
		Gateways:         gateways.Items,
		GatewayClasses:   classes.Items,
		ControllerName:   p.ControllerName,
		DefaultSite:      p.DefaultSite,
		KnownSites:       knownSites,
		PrivateResources: resources.Items,
		Licensed:         p.Licensed,
		ResourcePolicies: policies.Items,
	}
	inputs.KnownMachines, inputs.KnownUsers = p.knownGrantees(ctx, routes.Items, resources.Items)

	// Credentials come before the render because the render is pure. A failure
	// here aborts the pass: rendering a protected route without its credentials
	// would drop it from the blueprint, and the prune would then unpublish it.
	basicAuth, err := p.resolveBasicAuth(ctx, gateway.ServedRoutes(inputs))
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("resolving basic auth credentials: %w", err)
	}
	inputs.BasicAuth = basicAuth

	mtls, err := p.resolveMTLS(ctx, gateway.ServedRoutes(inputs))
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("resolving mTLS CAs: %w", err)
	}
	inputs.MTLS = mtls

	result := gateway.RenderAll(inputs)
	blueprint, verdicts := result.Blueprint, result.Routes

	resourcesDesired.WithLabelValues(visibilityPublic).
		Set(float64(len(blueprint.PublicResources)))
	resourcesDesired.WithLabelValues(visibilityPrivate).
		Set(float64(len(blueprint.PrivateResources)))
	resourcesDesired.WithLabelValues(visibilityPolicy).
		Set(float64(len(blueprint.PublicPolicies)))
	routesRejected.Set(float64(countRejected(verdicts)))
	privateResourcesRejected.Set(float64(countRejectedResources(result.PrivateResources)))
	policiesRejected.Set(float64(countRejectedResources(result.Policies)))

	publishErr := p.Pangolin.ApplyBlueprint(ctx, blueprint)
	if publishErr != nil {
		publishTotal.WithLabelValues("error").Inc()
		log.Error(publishErr, "applying blueprint",
			"publicResources", len(blueprint.PublicResources),
			"privateResources", len(blueprint.PrivateResources),
			"policies", len(blueprint.PublicPolicies))
		markPublishFailed(verdicts, publishErr)
	} else {
		publishTotal.WithLabelValues("success").Inc()
		log.V(1).Info("blueprint applied",
			"publicResources", len(blueprint.PublicResources),
			"privateResources", len(blueprint.PrivateResources))
		if p.DryRun {
			markDryRun(verdicts)
		}
	}

	// A security control that cannot be confirmed active is reported inactive.
	// Skipped in a dry run, where nothing was written to confirm.
	if publishErr == nil && !p.DryRun {
		p.verifyMTLS(ctx, inputs.MTLS, verdicts)
	}

	// The prune runs only on a successful apply. Renaming a route produces a new
	// key; pruning the old one after a failed apply would delete the live
	// resource before its replacement exists.
	var pruneErr error
	if publishErr == nil {
		pruneErr = p.prune(ctx, blueprint)
	} else {
		pruneSkippedTotal.WithLabelValues("apply_failed", visibilityPublic).Inc()
		pruneSkippedTotal.WithLabelValues("apply_failed", visibilityPrivate).Inc()
		if p.Licensed {
			pruneSkippedTotal.WithLabelValues("apply_failed", visibilityPolicy).Inc()
		}
	}

	statusErr := errors.Join(
		p.writeStatus(ctx, routes.Items, verdicts, gateways.Items, classes.Items),
		p.writeResourceStatus(ctx, resources.Items, result.PrivateResources, publishErr),
		p.writePolicyStatus(ctx, policies.Items, result.Policies, publishErr),
	)

	if err := errors.Join(publishErr, pruneErr, statusErr); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: p.resyncInterval()}, nil
}

// knownGrantees lists machine clients and users, each only while something
// names one. Like the site check, a listing that fails disables its check
// rather than every resource, and Pangolin stays the authority it already was.
func (p *Publisher) knownGrantees(
	ctx context.Context,
	routes []gatewayv1.HTTPRoute,
	resources []v1alpha1.PrivateResource,
) (machines, users map[string]bool) {
	log := ctrl.LoggerFrom(ctx)

	var wantMachines, wantUsers bool
	for _, r := range resources {
		wantMachines = wantMachines || len(r.Spec.Machines) > 0
		wantUsers = wantUsers || len(r.Spec.Users) > 0
	}
	for _, r := range routes {
		_, set := r.Annotations[gateway.AnnotationUsers]
		wantUsers = wantUsers || set
	}

	if wantMachines {
		listed, err := p.Pangolin.ListMachines(ctx)
		if err != nil {
			log.Error(err, "listing machine clients; machine names will not be checked before the apply")
		} else {
			machines = make(map[string]bool, len(listed))
			for _, m := range listed {
				machines[m.NiceID] = true
			}
		}
	}
	if wantUsers {
		listed, err := p.Pangolin.ListUsers(ctx)
		if err != nil {
			log.Error(err, "listing users; user names will not be checked before the apply")
		} else {
			users = make(map[string]bool, 2*len(listed))
			for _, u := range listed {
				for _, id := range []string{u.Username, u.Email} {
					if id != "" {
						users[id] = true
					}
				}
			}
		}
	}
	return machines, users
}

func (p *Publisher) resyncInterval() time.Duration {
	if p.ResyncInterval > 0 {
		return p.ResyncInterval
	}
	return DefaultResyncInterval
}

// prune removes resources this controller owns that no route claims any more.
// It touches nothing without the gw- prefix, because a hand-written blueprint
// maintains other resources in the same organisation.
//
// The two sections are separate listings over separate tables, so they prune
// independently: a private listing that fails says nothing about which public
// resources are still wanted.
func (p *Publisher) prune(ctx context.Context, desired pangolin.Blueprint) error {
	errs := []error{
		pruneSection(ctx, visibilityPublic, desired.PublicResources,
			p.Pangolin.ListPublicResources, p.Pangolin.DeletePublicResource, false),
		pruneSection(ctx, visibilityPrivate, desired.PrivateResources,
			p.Pangolin.ListPrivateResources, p.Pangolin.DeletePrivateResource, false),
	}
	// Last, because Pangolin refuses to delete a policy a resource still names,
	// and the resources pruned above are the ones that may have named it. An
	// unlicensed organisation has no policy API to list.
	if p.Licensed {
		errs = append(errs, pruneSection(ctx, visibilityPolicy, desired.PublicPolicies,
			p.Pangolin.ListResourcePolicies, p.Pangolin.DeleteResourcePolicy, true))
	}
	return errors.Join(errs...)
}

func pruneSection[T any](
	ctx context.Context,
	visibility string,
	desired map[string]T,
	list func(context.Context) ([]pangolin.Resource, error),
	del func(context.Context, int) error,
	tolerateDeleteFailure bool,
) error {
	log := ctrl.LoggerFrom(ctx).WithValues("visibility", visibility)

	// A listing that fails halfway would look like "these resources are gone".
	// Any error skips this section entirely rather than deleting on a partial view.
	live, err := list(ctx)
	if err != nil {
		pruneSkippedTotal.WithLabelValues("list_failed", visibility).Inc()
		return fmt.Errorf("listing %s resources for the prune: %w", visibility, err)
	}

	var failures []error
	for _, r := range live {
		if !strings.HasPrefix(r.NiceID, gateway.KeyPrefix) {
			continue
		}
		if _, wanted := desired[r.NiceID]; wanted {
			continue
		}

		if err := del(ctx, r.ResourceID); err != nil {
			// Counted and retried next pass, never fatal: a blocked DELETE must not
			// take the blueprint apply down with it.
			pruneTotal.WithLabelValues("error", visibility).Inc()
			log.Error(err, "deleting orphaned resource", "niceId", r.NiceID, "resourceId", r.ResourceID)
			// A refused delete is retried on the next pass and does not fail this one.
			if !tolerateDeleteFailure {
				failures = append(failures, err)
			}
			continue
		}
		pruneTotal.WithLabelValues("success", visibility).Inc()
		log.Info("deleted orphaned resource", "niceId", r.NiceID, "resourceId", r.ResourceID)
	}
	return errors.Join(failures...)
}

// writeStatus updates every route this controller has an opinion about, plus the
// Gateways and GatewayClasses it serves. Writes happen only where something
// actually changed, or the write itself would trigger the next pass.
func (p *Publisher) writeStatus(
	ctx context.Context,
	routes []gatewayv1.HTTPRoute,
	verdicts map[types.NamespacedName]gateway.Verdict,
	gateways []gatewayv1.Gateway,
	classes []gatewayv1.GatewayClass,
) error {
	log := ctrl.LoggerFrom(ctx)
	var errs []error

	for i := range routes {
		r := routes[i]
		key := types.NamespacedName{Namespace: r.Namespace, Name: r.Name}
		// A route with no verdict is one we do not serve; SetRouteStatus then
		// removes our stale entries and leaves every other controller's alone.
		if !gateway.SetRouteStatus(&r, verdicts[key], p.ControllerName) {
			continue
		}
		if err := p.Client.Status().Update(ctx, &r); err != nil {
			log.Error(err, "updating HTTPRoute status", "route", key)
			errs = append(errs, err)
		}
	}

	served := servedClassNames(classes, p.ControllerName)
	for i := range classes {
		c := classes[i]
		if !served[c.Name] {
			continue
		}
		if !setConditions(&c.Status.Conditions, c.Generation, condition{
			typ:     string(gatewayv1.GatewayClassConditionStatusAccepted),
			status:  metav1.ConditionTrue,
			reason:  string(gatewayv1.GatewayClassReasonAccepted),
			message: "Served by pangolin-gateway",
		}) {
			continue
		}
		if err := p.Client.Status().Update(ctx, &c); err != nil {
			log.Error(err, "updating GatewayClass status", "gatewayClass", c.Name)
			errs = append(errs, err)
		}
	}

	for i := range gateways {
		g := gateways[i]
		if !served[string(g.Spec.GatewayClassName)] {
			continue
		}
		if !setConditions(&g.Status.Conditions, g.Generation,
			condition{
				typ:     string(gatewayv1.GatewayConditionAccepted),
				status:  metav1.ConditionTrue,
				reason:  string(gatewayv1.GatewayReasonAccepted),
				message: "Served by pangolin-gateway",
			},
			condition{
				typ:     string(gatewayv1.GatewayConditionProgrammed),
				status:  metav1.ConditionTrue,
				reason:  string(gatewayv1.GatewayReasonProgrammed),
				message: "Routes are published to Pangolin",
			},
		) {
			continue
		}
		if err := p.Client.Status().Update(ctx, &g); err != nil {
			log.Error(err, "updating Gateway status",
				"gateway", types.NamespacedName{Namespace: g.Namespace, Name: g.Name})
			errs = append(errs, err)
		}
	}

	return errors.Join(errs...)
}

// writeResourceStatus brings each PrivateResource's conditions in line with its
// verdict and the outcome of the apply, writing only where something changed.
func (p *Publisher) writeResourceStatus(
	ctx context.Context,
	resources []v1alpha1.PrivateResource,
	verdicts map[types.NamespacedName]gateway.ResourceVerdict,
	publishErr error,
) error {
	log := ctrl.LoggerFrom(ctx)
	var errs []error

	for i := range resources {
		r := resources[i]
		key := types.NamespacedName{Namespace: r.Namespace, Name: r.Name}
		v := verdicts[key]

		changed := setConditions(&r.Status.Conditions, r.Generation,
			condition{
				typ:     v1alpha1.ConditionAccepted,
				status:  v.Accepted.Status,
				reason:  v.Accepted.Reason,
				message: v.Accepted.Message,
			},
			p.programmed(v, publishErr),
		)
		// Empty once rejected. A failed apply changes nothing: a resource that
		// was never applied has no key yet, and a live one keeps its own.
		resourceKey := r.Status.ResourceKey
		switch {
		case !v.Published():
			resourceKey = ""
		case publishErr == nil:
			resourceKey = v.Key
		}
		if r.Status.ResourceKey != resourceKey {
			r.Status.ResourceKey = resourceKey
			changed = true
		}
		if !changed {
			continue
		}
		if err := p.Client.Status().Update(ctx, &r); err != nil {
			log.Error(err, "updating PrivateResource status", "privateResource", key)
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// programmed derives the Programmed condition of a PrivateResource or
// ResourcePolicy from its verdict and the outcome of the apply.
func (p *Publisher) programmed(v gateway.ResourceVerdict, publishErr error) condition {
	c := condition{
		typ:     v1alpha1.ConditionProgrammed,
		status:  metav1.ConditionTrue,
		reason:  gateway.ReasonProgrammed,
		message: "Published to Pangolin as " + v.Key,
	}
	switch {
	case !v.Published():
		c.status = metav1.ConditionFalse
		c.reason = gateway.ReasonNotAccepted
		c.message = "Not published: " + v.Accepted.Message
	case publishErr != nil:
		c.status = metav1.ConditionFalse
		c.reason = gateway.ReasonPublishFailed
		c.message = fmt.Sprintf("publishing to Pangolin failed: %v", publishErr)
	case p.DryRun:
		c.status = metav1.ConditionFalse
		c.reason = gateway.ReasonDryRun
		c.message = "dry run: accepted but not published to Pangolin"
	}
	return c
}

// writePolicyStatus is writeResourceStatus for ResourcePolicies.
func (p *Publisher) writePolicyStatus(
	ctx context.Context,
	policies []v1alpha1.ResourcePolicy,
	verdicts map[types.NamespacedName]gateway.ResourceVerdict,
	publishErr error,
) error {
	log := ctrl.LoggerFrom(ctx)
	var errs []error

	for i := range policies {
		rp := policies[i]
		key := types.NamespacedName{Namespace: rp.Namespace, Name: rp.Name}
		v := verdicts[key]

		changed := setConditions(&rp.Status.Conditions, rp.Generation,
			condition{
				typ:     v1alpha1.ConditionAccepted,
				status:  v.Accepted.Status,
				reason:  v.Accepted.Reason,
				message: v.Accepted.Message,
			},
			p.programmed(v, publishErr),
		)
		policyKey := rp.Status.PolicyKey
		switch {
		case !v.Published():
			policyKey = ""
		case publishErr == nil:
			policyKey = v.Key
		}
		if rp.Status.PolicyKey != policyKey {
			rp.Status.PolicyKey = policyKey
			changed = true
		}
		if !changed {
			continue
		}
		if err := p.Client.Status().Update(ctx, &rp); err != nil {
			log.Error(err, "updating ResourcePolicy status", "resourcePolicy", key)
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

type condition struct {
	typ     string
	status  metav1.ConditionStatus
	reason  string
	message string
}

func setConditions(target *[]metav1.Condition, generation int64, conds ...condition) bool {
	var changed bool
	for _, c := range conds {
		if meta.SetStatusCondition(target, metav1.Condition{
			Type:               c.typ,
			Status:             c.status,
			Reason:             c.reason,
			Message:            c.message,
			ObservedGeneration: generation,
		}) {
			changed = true
		}
	}
	return changed
}

// markPublishFailed flips the routes that were in the failed blueprint. Routes
// the renderer had already rejected keep their own, more specific reason.
func markPublishFailed(verdicts map[types.NamespacedName]gateway.Verdict, err error) {
	for key, v := range verdicts {
		if !v.Published() {
			continue
		}
		v.Accepted = gateway.ConditionResult{
			Status:  metav1.ConditionFalse,
			Reason:  gateway.ReasonPublishFailed,
			Message: fmt.Sprintf("publishing to Pangolin failed: %v", err),
		}
		verdicts[key] = v
	}
}

// markDryRun keeps Accepted true — the route is accepted, it simply was not
// published — but says so in the reason, so nobody reads a rehearsal as a rollout.
func markDryRun(verdicts map[types.NamespacedName]gateway.Verdict) {
	for key, v := range verdicts {
		if !v.Published() {
			continue
		}
		v.Accepted = gateway.ConditionResult{
			Status:  metav1.ConditionTrue,
			Reason:  gateway.ReasonDryRun,
			Message: "dry run: accepted but not published to Pangolin",
		}
		verdicts[key] = v
	}
}

func countRejected(verdicts map[types.NamespacedName]gateway.Verdict) int {
	var n int
	for _, v := range verdicts {
		if !v.Published() {
			n++
		}
	}
	return n
}

func countRejectedResources(verdicts map[types.NamespacedName]gateway.ResourceVerdict) int {
	var n int
	for _, v := range verdicts {
		if !v.Published() {
			n++
		}
	}
	return n
}

func servedClassNames(classes []gatewayv1.GatewayClass, controllerName string) map[string]bool {
	served := make(map[string]bool, len(classes))
	for _, c := range classes {
		if string(c.Spec.ControllerName) == controllerName {
			served[c.Name] = true
		}
	}
	return served
}
