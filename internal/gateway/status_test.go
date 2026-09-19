// SPDX-License-Identifier: AGPL-3.0-only

package gateway

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

func parentStatus(controller, name string, conds ...metav1.Condition) gatewayv1.RouteParentStatus {
	return gatewayv1.RouteParentStatus{
		ParentRef:      gatewayv1.ParentReference{Name: gatewayv1.ObjectName(name)},
		ControllerName: gatewayv1.GatewayController(controller),
		Conditions:     conds,
	}
}

func servedVerdict() Verdict {
	return Verdict{
		Key:          "gw-demo-web",
		Generation:   3,
		ParentRefs:   []gatewayv1.ParentReference{{Name: "gw"}},
		Accepted:     accepted(),
		ResolvedRefs: resolved(),
	}
}

func conditionOf(t *testing.T, ps gatewayv1.RouteParentStatus, typ string) metav1.Condition {
	t.Helper()

	for _, c := range ps.Conditions {
		if c.Type == typ {
			return c
		}
	}
	t.Fatalf("no %s condition on parent %s; have %+v", typ, ps.ParentRef.Name, ps.Conditions)
	return metav1.Condition{}
}

func TestSetRouteStatusWritesOneEntryPerServedParent(t *testing.T) {
	r := route("demo", "web")
	r.Generation = 3

	if changed := SetRouteStatus(&r, servedVerdict(), testControllerName); !changed {
		t.Error("SetRouteStatus reported no change on a route with no status at all")
	}

	if len(r.Status.Parents) != 1 {
		t.Fatalf("got %d parent entries, want 1: %+v", len(r.Status.Parents), r.Status.Parents)
	}
	ps := r.Status.Parents[0]
	if got, want := string(ps.ControllerName), testControllerName; got != want {
		t.Errorf("controllerName = %q, want %q", got, want)
	}

	for _, typ := range []string{
		string(gatewayv1.RouteConditionAccepted),
		string(gatewayv1.RouteConditionResolvedRefs),
	} {
		c := conditionOf(t, ps, typ)
		if c.Status != metav1.ConditionTrue {
			t.Errorf("%s = %s, want True", typ, c.Status)
		}
		if c.ObservedGeneration != 3 {
			t.Errorf("%s observedGeneration = %d, want 3", typ, c.ObservedGeneration)
		}
	}
}

// Another controller's entry on the same route must survive untouched; clobbering
// it would break whoever owns that parent.
func TestSetRouteStatusLeavesForeignEntriesAlone(t *testing.T) {
	foreign := parentStatus("example.com/other", "other-gw", metav1.Condition{
		Type:               string(gatewayv1.RouteConditionAccepted),
		Status:             metav1.ConditionTrue,
		Reason:             "Accepted",
		LastTransitionTime: metav1.NewTime(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)),
	})
	r := route("demo", "web")
	r.Status.Parents = []gatewayv1.RouteParentStatus{foreign}

	SetRouteStatus(&r, servedVerdict(), testControllerName)

	if len(r.Status.Parents) != 2 {
		t.Fatalf("got %d parent entries, want the foreign one plus ours: %+v",
			len(r.Status.Parents), r.Status.Parents)
	}
	var found bool
	for _, ps := range r.Status.Parents {
		if string(ps.ControllerName) != "example.com/other" {
			continue
		}
		found = true
		if len(ps.Conditions) != 1 || ps.Conditions[0].Reason != "Accepted" {
			t.Errorf("foreign entry was modified: %+v", ps)
		}
	}
	if !found {
		t.Error("the foreign controller's entry disappeared")
	}
}

// When a route stops naming a parent we serve, our stale entry has to go, or it
// claims forever that we accepted a route we no longer publish.
func TestSetRouteStatusRemovesOurStaleEntries(t *testing.T) {
	r := route("demo", "web")
	r.Status.Parents = []gatewayv1.RouteParentStatus{
		parentStatus(testControllerName, "gw"),
		parentStatus(testControllerName, "gone"),
		parentStatus("example.com/other", "other-gw"),
	}

	changed := SetRouteStatus(&r, servedVerdict(), testControllerName)
	if !changed {
		t.Error("removing a stale entry was not reported as a change")
	}

	for _, ps := range r.Status.Parents {
		if string(ps.ControllerName) == testControllerName && ps.ParentRef.Name == "gone" {
			t.Error("stale entry for parent \"gone\" survived")
		}
	}
	if len(r.Status.Parents) != 2 {
		t.Errorf("got %d entries, want ours for \"gw\" plus the foreign one: %+v",
			len(r.Status.Parents), r.Status.Parents)
	}
}

// A route that is no longer ours at all keeps only the other controllers' entries.
func TestSetRouteStatusWithNoServedParentsDropsAllOfOurs(t *testing.T) {
	r := route("demo", "web")
	r.Status.Parents = []gatewayv1.RouteParentStatus{
		parentStatus(testControllerName, "gw"),
		parentStatus("example.com/other", "other-gw"),
	}

	changed := SetRouteStatus(&r, Verdict{}, testControllerName)
	if !changed {
		t.Error("dropping our entries was not reported as a change")
	}
	if len(r.Status.Parents) != 1 {
		t.Fatalf("got %d entries, want only the foreign one: %+v",
			len(r.Status.Parents), r.Status.Parents)
	}
	if string(r.Status.Parents[0].ControllerName) == testControllerName {
		t.Error("our own entry survived")
	}
}

// Without this the controller hot-loops: every status write is a watch event,
// which triggers a pass, which writes status again.
func TestSetRouteStatusIsIdempotent(t *testing.T) {
	r := route("demo", "web")
	r.Generation = 3

	if changed := SetRouteStatus(&r, servedVerdict(), testControllerName); !changed {
		t.Fatal("first write reported no change")
	}
	before := r.DeepCopy()

	for i := range 5 {
		if changed := SetRouteStatus(&r, servedVerdict(), testControllerName); changed {
			t.Fatalf("repeat write %d reported a change on identical input", i+1)
		}
	}

	beforeCond := conditionOf(t, before.Status.Parents[0], string(gatewayv1.RouteConditionAccepted))
	afterCond := conditionOf(t, r.Status.Parents[0], string(gatewayv1.RouteConditionAccepted))
	if !beforeCond.LastTransitionTime.Equal(&afterCond.LastTransitionTime) {
		t.Errorf("lastTransitionTime moved from %v to %v without a status change",
			beforeCond.LastTransitionTime, afterCond.LastTransitionTime)
	}
}

func TestSetRouteStatusReportsRealChanges(t *testing.T) {
	r := route("demo", "web")
	r.Generation = 3
	SetRouteStatus(&r, servedVerdict(), testControllerName)

	failed := servedVerdict()
	failed.Accepted = ConditionResult{
		Status:  metav1.ConditionFalse,
		Reason:  ReasonPublishFailed,
		Message: "pangolin unreachable",
	}

	if changed := SetRouteStatus(&r, failed, testControllerName); !changed {
		t.Fatal("a flip to Accepted=False was not reported as a change")
	}
	c := conditionOf(t, r.Status.Parents[0], string(gatewayv1.RouteConditionAccepted))
	if c.Status != metav1.ConditionFalse || c.Reason != ReasonPublishFailed {
		t.Errorf("Accepted = %+v, want False/%s", c, ReasonPublishFailed)
	}
}

// A generation bump alone must reach the status, or observedGeneration lags and
// ArgoCD keeps reporting the route as still progressing.
func TestSetRouteStatusTracksObservedGeneration(t *testing.T) {
	r := route("demo", "web")
	r.Generation = 3
	SetRouteStatus(&r, servedVerdict(), testControllerName)

	v := servedVerdict()
	v.Generation = 4
	r.Generation = 4

	if changed := SetRouteStatus(&r, v, testControllerName); !changed {
		t.Fatal("a generation bump was not reported as a change")
	}
	c := conditionOf(t, r.Status.Parents[0], string(gatewayv1.RouteConditionAccepted))
	if c.ObservedGeneration != 4 {
		t.Errorf("observedGeneration = %d, want 4", c.ObservedGeneration)
	}
}

// Entry order must not depend on map iteration, or every pass rewrites the status
// with the same content in a different order.
func TestSetRouteStatusOrdersEntriesDeterministically(t *testing.T) {
	v := servedVerdict()
	v.ParentRefs = []gatewayv1.ParentReference{
		{Name: "zebra"}, {Name: "alpha"}, {Name: "middle"},
	}

	var first []string
	for i := range 10 {
		r := route("demo", "web")
		SetRouteStatus(&r, v, testControllerName)

		var names []string
		for _, ps := range r.Status.Parents {
			names = append(names, string(ps.ParentRef.Name))
		}
		if i == 0 {
			first = names
			continue
		}
		if len(names) != len(first) {
			t.Fatalf("entry count changed: %v then %v", first, names)
		}
		for j := range names {
			if names[j] != first[j] {
				t.Fatalf("entry order changed: %v then %v", first, names)
			}
		}
	}
}
