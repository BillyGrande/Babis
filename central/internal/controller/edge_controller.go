package controller

import (
	"context"
	"encoding/json"
	"time"

	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
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
	var edge v1alpha1.Edge
	if err := r.Client.Get(ctx, req.NamespacedName, &edge); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
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

	rs, rr, rm, requeue := Reachability(last, timeout, now())
	as, ar, am := ArgoCDReadiness(last, edge.Status.ArgoCD.Components)
	if rs == metav1.ConditionFalse {
		// The last reported components may be arbitrarily old.
		as, ar, am = metav1.ConditionUnknown, "HeartbeatStale", "Edge is unreachable; its last reported ArgoCD state may be stale"
	}

	conds := append([]metav1.Condition(nil), edge.Status.Conditions...)
	meta.SetStatusCondition(&conds, metav1.Condition{
		Type: v1alpha1.ConditionReachable, Status: rs, Reason: rr, Message: rm,
		ObservedGeneration: edge.Generation,
	})
	meta.SetStatusCondition(&conds, metav1.Condition{
		Type: v1alpha1.ConditionArgoCDReady, Status: as, Reason: ar, Message: am,
		ObservedGeneration: edge.Generation,
	})
	if !equality.Semantic.DeepEqual(edge.Status.Conditions, conds) {
		if err := r.applyConditions(ctx, &edge, conds); err != nil {
			if apierrors.IsNotFound(err) {
				return ctrl.Result{}, nil
			}
			return ctrl.Result{}, err
		}
	}
	return ctrl.Result{RequeueAfter: requeue}, nil
}

// fieldManager owns status.conditions and nothing else.
const fieldManager = "babis-reconciler"

// conditionsApply is the server-side apply body: identity plus conditions.
type conditionsApply struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Metadata   struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
	} `json:"metadata"`
	Status struct {
		Conditions []metav1.Condition `json:"conditions"`
	} `json:"status"`
}

func buildConditionsApply(edge *v1alpha1.Edge, conds []metav1.Condition) conditionsApply {
	var a conditionsApply
	a.APIVersion = v1alpha1.GroupVersion.String()
	a.Kind = "Edge"
	a.Metadata.Name, a.Metadata.Namespace = edge.Name, edge.Namespace
	a.Status.Conditions = conds
	return a
}

func (r *EdgeReconciler) applyConditions(ctx context.Context, edge *v1alpha1.Edge, conds []metav1.Condition) error {
	body, err := json.Marshal(buildConditionsApply(edge, conds))
	if err != nil {
		return err
	}
	target := &v1alpha1.Edge{ObjectMeta: metav1.ObjectMeta{Namespace: edge.Namespace, Name: edge.Name}}
	return r.Client.Status().Patch(ctx, target, client.RawPatch(types.ApplyPatchType, body),
		client.FieldOwner(fieldManager), client.ForceOwnership)
}

// SetupWithManager registers the reconciler.
func (r *EdgeReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).For(&v1alpha1.Edge{}).Complete(r)
}
