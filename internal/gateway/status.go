// SPDX-License-Identifier: AGPL-3.0-only

package gateway

import (
	"sort"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// SetRouteStatus brings the route's status in line with the verdict and reports
// whether anything actually changed.
//
// An HTTPRoute's status is per parentRef: each entry carries the controllerName
// that wrote it, and a controller must touch only its own. The caller must skip
// the write entirely when this returns false — a status write is itself a watch
// event, so writing unchanged status would loop the controller against itself.
func SetRouteStatus(r *gatewayv1.HTTPRoute, v Verdict, controllerName string) bool {
	// Keyed by string, not by the normalised struct: ParentReference holds
	// pointer fields, so struct equality compares addresses rather than values.
	wanted := make(map[string]bool, len(v.ParentRefs))
	for _, ref := range v.ParentRefs {
		wanted[refKey(ref, r.Namespace)] = true
	}

	var (
		kept    []gatewayv1.RouteParentStatus
		changed bool
	)

	// Other controllers' entries pass through untouched; ours survive only while
	// the route still names that parent.
	for _, ps := range r.Status.Parents {
		if string(ps.ControllerName) != controllerName {
			kept = append(kept, ps)
			continue
		}
		if wanted[refKey(ps.ParentRef, r.Namespace)] {
			kept = append(kept, ps)
			continue
		}
		changed = true
	}

	for _, ref := range v.ParentRefs {
		key := refKey(ref, r.Namespace)

		idx := -1
		for i, ps := range kept {
			if string(ps.ControllerName) == controllerName &&
				refKey(ps.ParentRef, r.Namespace) == key {
				idx = i
				break
			}
		}
		if idx < 0 {
			kept = append(kept, gatewayv1.RouteParentStatus{
				ParentRef:      ref,
				ControllerName: gatewayv1.GatewayController(controllerName),
			})
			idx = len(kept) - 1
			changed = true
		}

		for _, c := range []struct {
			typ    gatewayv1.RouteConditionType
			result ConditionResult
		}{
			{gatewayv1.RouteConditionAccepted, v.Accepted},
			{gatewayv1.RouteConditionResolvedRefs, v.ResolvedRefs},
		} {
			// SetStatusCondition keeps lastTransitionTime when only the message or
			// observedGeneration moves, so an unchanged condition stays byte-identical.
			if meta.SetStatusCondition(&kept[idx].Conditions, metav1.Condition{
				Type:               string(c.typ),
				Status:             c.result.Status,
				Reason:             c.result.Reason,
				Message:            c.result.Message,
				ObservedGeneration: v.Generation,
			}) {
				changed = true
			}
		}
	}

	// A stable order keeps repeated passes byte-identical; map iteration would
	// otherwise rewrite the same content in a different sequence every time.
	sort.SliceStable(kept, func(i, j int) bool {
		a, b := kept[i], kept[j]
		if a.ControllerName != b.ControllerName {
			return a.ControllerName < b.ControllerName
		}
		return refKey(a.ParentRef, r.Namespace) < refKey(b.ParentRef, r.Namespace)
	})

	if !sameOrder(r.Status.Parents, kept, r.Namespace) {
		changed = true
	}
	r.Status.Parents = kept
	return changed
}

// normaliseRef fills in the defaults the API server leaves implicit, so a ref
// spelled out in full and the same ref written tersely compare equal.
func normaliseRef(
	ref gatewayv1.ParentReference,
	routeNamespace string,
) gatewayv1.ParentReference {
	out := gatewayv1.ParentReference{Name: ref.Name}

	group := gatewayv1.Group(gatewayv1.GroupName)
	if ref.Group != nil && *ref.Group != "" {
		group = *ref.Group
	}
	out.Group = &group

	kind := gatewayv1.Kind("Gateway")
	if ref.Kind != nil {
		kind = *ref.Kind
	}
	out.Kind = &kind

	ns := gatewayv1.Namespace(routeNamespace)
	if ref.Namespace != nil {
		ns = *ref.Namespace
	}
	out.Namespace = &ns

	out.SectionName = ref.SectionName
	out.Port = ref.Port
	return out
}

func refKey(ref gatewayv1.ParentReference, routeNamespace string) string {
	n := normaliseRef(ref, routeNamespace)
	key := string(*n.Group) + "/" + string(*n.Kind) + "/" + string(*n.Namespace) + "/" + string(n.Name)
	if n.SectionName != nil {
		key += "/" + string(*n.SectionName)
	}
	return key
}

func sameOrder(a, b []gatewayv1.RouteParentStatus, routeNamespace string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].ControllerName != b[i].ControllerName ||
			refKey(a[i].ParentRef, routeNamespace) != refKey(b[i].ParentRef, routeNamespace) {
			return false
		}
	}
	return true
}
