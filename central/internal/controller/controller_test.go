package controller

import (
	"context"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/BillyGrande/babis/central/api/v1alpha1"
)

var t0 = time.Date(2026, 10, 30, 10, 0, 0, 0, time.UTC)

func TestReachability(t *testing.T) {
	timeout := 15 * time.Minute
	last := t0
	tests := []struct {
		name    string
		last    *time.Time
		now     time.Time
		status  metav1.ConditionStatus
		reason  string
		requeue time.Duration
	}{
		{"never", nil, t0, metav1.ConditionUnknown, "NeverCheckedIn", 0},
		{"fresh", &last, t0.Add(5 * time.Minute), metav1.ConditionTrue, "HeartbeatFresh", 10 * time.Minute},
		{"just before deadline", &last, t0.Add(timeout - time.Nanosecond), metav1.ConditionTrue, "HeartbeatFresh", time.Nanosecond},
		{"exactly deadline", &last, t0.Add(timeout), metav1.ConditionFalse, "HeartbeatStale", 0},
		{"stale", &last, t0.Add(time.Hour), metav1.ConditionFalse, "HeartbeatStale", 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, r, m, rq := Reachability(tc.last, timeout, tc.now)
			if s != tc.status || r != tc.reason || rq != tc.requeue || m == "" {
				t.Errorf("got %v %q %q %v, want %v %q %v", s, r, m, rq, tc.status, tc.reason, tc.requeue)
			}
		})
	}
}

func TestArgoCDReadiness(t *testing.T) {
	last := t0
	ok := v1alpha1.Component{Name: "a", Ready: 1, Desired: 1}
	tests := []struct {
		name     string
		last     *time.Time
		comps    []v1alpha1.Component
		status   metav1.ConditionStatus
		reason   string
		contains []string
	}{
		{"no checkin", nil, []v1alpha1.Component{ok}, metav1.ConditionUnknown, "NeverCheckedIn", nil},
		{"no checkin no comps", nil, nil, metav1.ConditionUnknown, "NeverCheckedIn", nil},
		{"empty", &last, nil, metav1.ConditionFalse, "NoComponents", nil},
		{"all ready", &last, []v1alpha1.Component{ok, {Name: "b", Ready: 3, Desired: 3}}, metav1.ConditionTrue, "AllComponentsReady", nil},
		{"one not ready", &last, []v1alpha1.Component{ok, {Name: "b", Ready: 1, Desired: 2}}, metav1.ConditionFalse, "ComponentsNotReady", []string{"b (1/2)"}},
		{"scaled to zero", &last, []v1alpha1.Component{ok, {Name: "z", Ready: 0, Desired: 0}}, metav1.ConditionFalse, "ComponentsNotReady", []string{"z (0/0)"}},
		{"two not ready", &last, []v1alpha1.Component{{Name: "x", Ready: 0, Desired: 1}, {Name: "y", Ready: 2, Desired: 1}}, metav1.ConditionFalse, "ComponentsNotReady", []string{"x", "y"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, r, m := ArgoCDReadiness(tc.last, tc.comps)
			if s != tc.status || r != tc.reason {
				t.Fatalf("got %v %q (%s), want %v %q", s, r, m, tc.status, tc.reason)
			}
			for _, c := range tc.contains {
				if !strings.Contains(m, c) {
					t.Errorf("message %q lacks %q", m, c)
				}
			}
		})
	}
}

func TestReconcile(t *testing.T) {
	now := t0.Add(5 * time.Minute)
	checkin := func(ago time.Duration) *metav1.Time {
		m := metav1.NewTime(now.Add(-ago))
		return &m
	}
	healthy := v1alpha1.ArgoCDStatus{Components: []v1alpha1.Component{{Name: "a", Ready: 1, Desired: 1}}}
	tests := []struct {
		name      string
		edge      v1alpha1.Edge
		reachable metav1.ConditionStatus
		ready     metav1.ConditionStatus
		requeue   time.Duration
	}{
		{"fresh", v1alpha1.Edge{Status: v1alpha1.EdgeStatus{LastCheckinTime: checkin(time.Minute), ArgoCD: healthy}},
			metav1.ConditionTrue, metav1.ConditionTrue, 14 * time.Minute},
		{"stale", v1alpha1.Edge{Status: v1alpha1.EdgeStatus{LastCheckinTime: checkin(time.Hour), ArgoCD: healthy}},
			metav1.ConditionFalse, metav1.ConditionTrue, 0},
		{"never", v1alpha1.Edge{},
			metav1.ConditionUnknown, metav1.ConditionUnknown, 0},
		{"spec timeout shortens", v1alpha1.Edge{
			Spec:   v1alpha1.EdgeSpec{HeartbeatTimeout: &metav1.Duration{Duration: time.Minute}},
			Status: v1alpha1.EdgeStatus{LastCheckinTime: checkin(2 * time.Minute), ArgoCD: healthy}},
			metav1.ConditionFalse, metav1.ConditionTrue, 0},
		{"spec timeout lengthens", v1alpha1.Edge{
			Spec:   v1alpha1.EdgeSpec{HeartbeatTimeout: &metav1.Duration{Duration: 4 * time.Hour}},
			Status: v1alpha1.EdgeStatus{LastCheckinTime: checkin(time.Hour), ArgoCD: healthy}},
			metav1.ConditionTrue, metav1.ConditionTrue, 3 * time.Hour},
		{"component down", v1alpha1.Edge{Status: v1alpha1.EdgeStatus{LastCheckinTime: checkin(time.Minute),
			ArgoCD: v1alpha1.ArgoCDStatus{Components: []v1alpha1.Component{{Name: "a", Ready: 0, Desired: 1}}}}},
			metav1.ConditionTrue, metav1.ConditionFalse, 14 * time.Minute},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			if err := v1alpha1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			tc.edge.Name, tc.edge.Namespace = "e1", "fleet"
			c := fake.NewClientBuilder().WithScheme(scheme).
				WithStatusSubresource(&v1alpha1.Edge{}).WithObjects(&tc.edge).Build()
			r := &EdgeReconciler{Client: c, DefaultHeartbeatTimeout: 15 * time.Minute, Now: func() time.Time { return now }}
			key := types.NamespacedName{Namespace: "fleet", Name: "e1"}

			res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key})
			if err != nil {
				t.Fatal(err)
			}
			if res.RequeueAfter != tc.requeue {
				t.Errorf("requeue %v, want %v", res.RequeueAfter, tc.requeue)
			}
			var got v1alpha1.Edge
			if err := c.Get(context.Background(), key, &got); err != nil {
				t.Fatal(err)
			}
			if s := meta.FindStatusCondition(got.Status.Conditions, v1alpha1.ConditionReachable); s == nil || s.Status != tc.reachable {
				t.Errorf("Reachable = %v, want %v", s, tc.reachable)
			}
			if s := meta.FindStatusCondition(got.Status.Conditions, v1alpha1.ConditionArgoCDReady); s == nil || s.Status != tc.ready {
				t.Errorf("ArgoCDReady = %v, want %v", s, tc.ready)
			}

			// A second pass with unchanged state must not write.
			rv := got.ResourceVersion
			if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
				t.Fatal(err)
			}
			if err := c.Get(context.Background(), key, &got); err != nil {
				t.Fatal(err)
			}
			if got.ResourceVersion != rv {
				t.Errorf("second reconcile wrote status (rv %s -> %s)", rv, got.ResourceVersion)
			}
		})
	}
}

func TestReconcileMissingEdge(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	r := &EdgeReconciler{Client: fake.NewClientBuilder().WithScheme(scheme).Build(), DefaultHeartbeatTimeout: time.Minute}
	res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "fleet", Name: "gone"}})
	if err != nil || res.RequeueAfter != 0 {
		t.Errorf("got %v, %v; want no error, no requeue", res, err)
	}
}
