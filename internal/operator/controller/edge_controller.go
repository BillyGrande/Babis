// Package controller reconciles Edge objects in the central cluster. The
// only actively reconciled property is reachability: edges push reports
// (which the ingest server writes to status), and this controller flips the
// Reachable condition when heartbeats stop arriving.
package controller

import (
	"context"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	fleetv1alpha1 "github.com/BillyGrande/babis/api/v1alpha1"
)

type EdgeReconciler struct {
	client.Client
	// DefaultHeartbeatTimeout applies to edges without spec.heartbeatTimeout.
	DefaultHeartbeatTimeout time.Duration
}

// +kubebuilder:rbac:groups=fleet.babis.dev,resources=edges,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=fleet.babis.dev,resources=edges/status,verbs=get;update;patch

func (r *EdgeReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var edge fleetv1alpha1.Edge
	if err := r.Get(ctx, req.NamespacedName, &edge); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	timeout := r.DefaultHeartbeatTimeout
	if edge.Spec.HeartbeatTimeout != nil {
		timeout = edge.Spec.HeartbeatTimeout.Duration
	}

	now := time.Now()
	reachable, checkAgain := Reachability(edge.Status.LastReportTime, timeout, now)

	desired := metav1.Condition{
		Type:               fleetv1alpha1.ConditionReachable,
		Status:             metav1.ConditionFalse,
		Reason:             "HeartbeatMissing",
		Message:            "no report received within the heartbeat timeout",
		ObservedGeneration: edge.Generation,
	}
	if reachable {
		desired.Status = metav1.ConditionTrue
		desired.Reason = "HeartbeatFresh"
		desired.Message = "reports are arriving within the heartbeat timeout"
	}

	if meta.SetStatusCondition(&edge.Status.Conditions, desired) {
		if err := r.Status().Update(ctx, &edge); err != nil {
			return ctrl.Result{}, err
		}
		log.Info("reachability changed", "edge", edge.Name, "reachable", reachable)
	}

	// Wake up again exactly when the current answer could change: at the
	// heartbeat deadline while reachable, at the next timeout interval when
	// not (a fresh report triggers reconcile immediately via the watch).
	return ctrl.Result{RequeueAfter: checkAgain}, nil
}

// Reachability decides whether an edge with the given last report time is
// reachable at now, and how long until that answer should be re-evaluated.
// Pure so it is trivially testable.
func Reachability(lastReport *metav1.Time, timeout time.Duration, now time.Time) (bool, time.Duration) {
	if lastReport == nil || lastReport.Time.IsZero() {
		return false, timeout
	}
	deadline := lastReport.Time.Add(timeout)
	if now.Before(deadline) {
		return true, deadline.Sub(now)
	}
	return false, timeout
}

func (r *EdgeReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&fleetv1alpha1.Edge{}).
		Named("edge").
		Complete(r)
}
