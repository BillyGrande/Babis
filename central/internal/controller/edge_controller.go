package controller

import (
	"context"
	"time"

	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/BillyGrande/babis/central/api/v1alpha1"
)

// EdgeReconciler owns only status.conditions of an Edge.
type EdgeReconciler struct {
	Client client.Client
	// DefaultHeartbeatTimeout is used when spec.heartbeatTimeout is unset.
	DefaultHeartbeatTimeout time.Duration
	// Now is the clock. Defaults to time.Now.
	Now func() time.Time
}

// +kubebuilder:rbac:groups=fleet.babis.dev,resources=edges,verbs=get;list;watch;create
// +kubebuilder:rbac:groups=fleet.babis.dev,resources=edges/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile sets the Reachable and ArgoCDReady conditions and requeues at the
// heartbeat deadline.
func (r *EdgeReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	now := time.Now
	if r.Now != nil {
		now = r.Now
	}
	var requeue time.Duration
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var edge v1alpha1.Edge
		if err := r.Client.Get(ctx, req.NamespacedName, &edge); err != nil {
			return err
		}
		timeout := r.DefaultHeartbeatTimeout
		if edge.Spec.HeartbeatTimeout != nil {
			timeout = edge.Spec.HeartbeatTimeout.Duration
		}
		var last *time.Time
		if edge.Status.LastCheckinTime != nil {
			t := edge.Status.LastCheckinTime.Time
			last = &t
		}

		rs, rr, rm, after := Reachability(last, timeout, now())
		requeue = after
		as, ar, am := ArgoCDReadiness(last, edge.Status.ArgoCD.Components)

		before := append([]metav1.Condition(nil), edge.Status.Conditions...)
		meta.SetStatusCondition(&edge.Status.Conditions, metav1.Condition{
			Type: v1alpha1.ConditionReachable, Status: rs, Reason: rr, Message: rm,
			ObservedGeneration: edge.Generation,
		})
		meta.SetStatusCondition(&edge.Status.Conditions, metav1.Condition{
			Type: v1alpha1.ConditionArgoCDReady, Status: as, Reason: ar, Message: am,
			ObservedGeneration: edge.Generation,
		})
		if equality.Semantic.DeepEqual(before, edge.Status.Conditions) {
			return nil
		}
		return r.Client.Status().Update(ctx, &edge)
	})
	if apierrors.IsNotFound(err) {
		return ctrl.Result{}, nil
	}
	if err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: requeue}, nil
}

// SetupWithManager registers the reconciler.
func (r *EdgeReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).For(&v1alpha1.Edge{}).Complete(r)
}
