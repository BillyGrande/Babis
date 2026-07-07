package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ConditionReachable is set on an Edge based on heartbeat freshness: True
// while reports arrive within the heartbeat timeout, False once they stop.
const ConditionReachable = "Reachable"

// EdgeSpec is the desired/expected state of one edge cluster. Edges are
// usually auto-registered by their first report; spec fields are operator
// overrides on top of that.
type EdgeSpec struct {
	// DisplayName is a human-friendly name shown in listings.
	// +optional
	DisplayName string `json:"displayName,omitempty"`

	// HeartbeatTimeout is how long after the last report the edge is
	// considered unreachable. Defaults to the operator-wide flag.
	// +optional
	HeartbeatTimeout *metav1.Duration `json:"heartbeatTimeout,omitempty"`

	// ExpectedStoreVersion is the air-gap store version this edge should be
	// running. When set and different from status.storeVersion, the edge is
	// out of date.
	// +optional
	ExpectedStoreVersion string `json:"expectedStoreVersion,omitempty"`
}

// AppSummary is the compact per-Application record reported by an edge.
type AppSummary struct {
	Name string `json:"name"`
	// Sync is the ArgoCD sync status (Synced, OutOfSync, Unknown).
	Sync string `json:"sync,omitempty"`
	// Health is the ArgoCD health status (Healthy, Degraded, Progressing, ...).
	Health string `json:"health,omitempty"`
	// Revision is the short revision currently deployed.
	Revision string `json:"revision,omitempty"`
}

// EdgeStatus is the last observed state, written by the ingest endpoint
// from edge reports and by the reconciler (reachability).
type EdgeStatus struct {
	// LastReportTime is when the most recent report was received.
	// +optional
	LastReportTime *metav1.Time `json:"lastReportTime,omitempty"`

	// Labels reported by the edge (site, region, hardware, ...).
	// +optional
	Labels map[string]string `json:"labels,omitempty"`

	// Apps is the per-Application state from the edge's ArgoCD.
	// +optional
	Apps []AppSummary `json:"apps,omitempty"`

	// AppsTotal / AppsSynced / AppsHealthy summarize Apps for printing and
	// selection without walking the list.
	// +optional
	AppsTotal int `json:"appsTotal,omitempty"`
	// +optional
	AppsSynced int `json:"appsSynced,omitempty"`
	// +optional
	AppsHealthy int `json:"appsHealthy,omitempty"`

	// StoreVersion is the air-gap store version reported by the edge.
	// +optional
	StoreVersion string `json:"storeVersion,omitempty"`

	// RegistryHealthy reports the edge's local OCI registry health, when the
	// edge runs one (air-gapped installs).
	// +optional
	RegistryHealthy *bool `json:"registryHealthy,omitempty"`

	// Conditions currently only carries Reachable.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=edge
// +kubebuilder:printcolumn:name="Display",type=string,JSONPath=`.spec.displayName`
// +kubebuilder:printcolumn:name="Apps",type=integer,JSONPath=`.status.appsTotal`
// +kubebuilder:printcolumn:name="Synced",type=integer,JSONPath=`.status.appsSynced`
// +kubebuilder:printcolumn:name="Healthy",type=integer,JSONPath=`.status.appsHealthy`
// +kubebuilder:printcolumn:name="Reachable",type=string,JSONPath=`.status.conditions[?(@.type=="Reachable")].status`
// +kubebuilder:printcolumn:name="Last Report",type=date,JSONPath=`.status.lastReportTime`

// Edge is one edge cluster in the fleet: auto-registered by its first
// report, kept fresh by heartbeats, marked unreachable when they stop.
type Edge struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   EdgeSpec   `json:"spec,omitempty"`
	Status EdgeStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// EdgeList contains a list of Edge.
type EdgeList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Edge `json:"items"`
}
