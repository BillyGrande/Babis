// Package controller holds the Edge reconciler and its pure decision logic.
package controller

import (
	"fmt"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/BillyGrande/babis/central/api/v1alpha1"
)

// Reachability decides the Reachable condition from the last check-in.
// An edge is reachable while now is strictly before lastCheckin+timeout; at
// exactly the deadline it is already stale. Messages carry no elapsed time,
// so an unchanged state produces no status write. For a reachable edge, requeueAfter
// is the time left until the deadline; otherwise it is zero.
func Reachability(lastCheckin *time.Time, timeout time.Duration, now time.Time) (status metav1.ConditionStatus, reason, message string, requeueAfter time.Duration) {
	if lastCheckin == nil {
		return metav1.ConditionUnknown, "NeverCheckedIn", "No check-in received yet", 0
	}
	deadline := lastCheckin.Add(timeout)
	if now.Before(deadline) {
		return metav1.ConditionTrue, "HeartbeatFresh",
			fmt.Sprintf("Last check-in is within the heartbeat timeout of %s", timeout),
			deadline.Sub(now)
	}
	return metav1.ConditionFalse, "HeartbeatStale",
		fmt.Sprintf("No check-in within the heartbeat timeout of %s", timeout), 0
}

// ArgoCDReadiness decides the ArgoCDReady condition from the reported
// components. It is True only when there is at least one component and every
// component wants replicas (desired > 0) and has them all (ready == desired).
func ArgoCDReadiness(lastCheckin *time.Time, comps []v1alpha1.Component) (status metav1.ConditionStatus, reason, message string) {
	if lastCheckin == nil {
		return metav1.ConditionUnknown, "NeverCheckedIn", "No check-in received yet"
	}
	if len(comps) == 0 {
		return metav1.ConditionFalse, "NoComponents", "The edge reported no ArgoCD components"
	}
	var notReady []string
	for _, c := range comps {
		if c.Desired <= 0 || c.Ready != c.Desired {
			notReady = append(notReady, fmt.Sprintf("%s (%d/%d)", c.Name, c.Ready, c.Desired))
		}
	}
	if len(notReady) > 0 {
		return metav1.ConditionFalse, "ComponentsNotReady", "Not ready: " + strings.Join(notReady, ", ")
	}
	return metav1.ConditionTrue, "AllComponentsReady", fmt.Sprintf("All %d components are ready", len(comps))
}
