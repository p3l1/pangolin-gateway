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
}

// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=httproutes;gateways;gatewayclasses,verbs=get;list;watch
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=httproutes/status;gateways/status;gatewayclasses/status,verbs=get;patch;update
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
		Complete(p)
}

func (p *Publisher) Reconcile(ctx context.Context, _ ctrl.Request) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)

	// Every read must succeed before anything is written. A partial view would
	// render a partial desired state, and the prune deletes on that result.
	var (
		classes  gatewayv1.GatewayClassList
		gateways gatewayv1.GatewayList
		routes   gatewayv1.HTTPRouteList
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

	blueprint, verdicts := gateway.Render(gateway.Inputs{
		Routes:         routes.Items,
		Gateways:       gateways.Items,
		GatewayClasses: classes.Items,
		ControllerName: p.ControllerName,
		DefaultSite:    p.DefaultSite,
		KnownSites:     knownSites,
	})

	resourcesDesired.Set(float64(len(blueprint.PublicResources)))
	routesRejected.Set(float64(countRejected(verdicts)))

	publishErr := p.Pangolin.ApplyBlueprint(ctx, blueprint)
	if publishErr != nil {
		publishTotal.WithLabelValues("error").Inc()
		log.Error(publishErr, "applying blueprint", "resources", len(blueprint.PublicResources))
		markPublishFailed(verdicts, publishErr)
	} else {
		publishTotal.WithLabelValues("success").Inc()
		log.V(1).Info("blueprint applied", "resources", len(blueprint.PublicResources))
	}

	// The prune runs only on a successful apply. Renaming a route produces a new
	// key; pruning the old one after a failed apply would delete the live
	// resource before its replacement exists.
	var pruneErr error
	if publishErr == nil {
		pruneErr = p.prune(ctx, blueprint)
	} else {
		pruneSkippedTotal.WithLabelValues("apply_failed").Inc()
	}

	statusErr := p.writeStatus(ctx, routes.Items, verdicts, gateways.Items, classes.Items)

	if err := errors.Join(publishErr, pruneErr, statusErr); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: p.resyncInterval()}, nil
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
func (p *Publisher) prune(ctx context.Context, desired pangolin.Blueprint) error {
	log := ctrl.LoggerFrom(ctx)

	// A listing that fails halfway would look like "these resources are gone".
	// Any error skips the prune entirely rather than deleting on a partial view.
	live, err := p.Pangolin.ListPublicResources(ctx)
	if err != nil {
		pruneSkippedTotal.WithLabelValues("list_failed").Inc()
		return fmt.Errorf("listing public resources for the prune: %w", err)
	}

	var failures []error
	for _, r := range live {
		if !strings.HasPrefix(r.NiceID, gateway.KeyPrefix) {
			continue
		}
		if _, wanted := desired.PublicResources[r.NiceID]; wanted {
			continue
		}

		if err := p.Pangolin.DeletePublicResource(ctx, r.ResourceID); err != nil {
			// Counted and retried next pass, never fatal: a blocked DELETE must not
			// take the blueprint apply down with it.
			pruneTotal.WithLabelValues("error").Inc()
			log.Error(err, "deleting orphaned resource", "niceId", r.NiceID, "resourceId", r.ResourceID)
			failures = append(failures, err)
			continue
		}
		pruneTotal.WithLabelValues("success").Inc()
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

func countRejected(verdicts map[types.NamespacedName]gateway.Verdict) int {
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
