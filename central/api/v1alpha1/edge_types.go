package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Condition types set by the Edge reconciler.
const (
	// ConditionReachable is True while check-ins arrive within the heartbeat timeout.
	ConditionReachable = "Reachable"
	// ConditionArgoCDReady is True while every reported ArgoCD component is ready.
	ConditionArgoCDReady = "ArgoCDReady"
)

// TelemetrySourceCheckin marks status written from a `babis checkin`.
const TelemetrySourceCheckin = "checkin"

// EdgeSpec is what admins say about the edge. Ingest never writes it.
type EdgeSpec struct {
	// DisplayName is a human-friendly name.
	// +optional
	DisplayName string `json:"displayName,omitempty"`
	// HeartbeatTimeout overrides the operator default. After this long without
	// a check-in the edge is not Reachable.
	// +optional
	HeartbeatTimeout *metav1.Duration `json:"heartbeatTimeout,omitempty"`
}

// Telemetry says where the status data came from.
type Telemetry struct {
	// Source is "checkin" in P1.
	// +optional
	Source string `json:"source,omitempty"`
}

// Component is one ArgoCD workload.
type Component struct {
	Name    string `json:"name"`
	Ready   int32  `json:"ready"`
	Desired int32  `json:"desired"`
}

// ArgoCDStatus is the observed ArgoCD install.
type ArgoCDStatus struct {
	// +optional
	Version string `json:"version,omitempty"`
	// +optional
	Components []Component `json:"components,omitempty"`
}

// App is one ArgoCD Application, with ArgoCD's own values.
type App struct {
	Name string `json:"name"`
	// +optional
	Sync string `json:"sync,omitempty"`
	// +optional
	Health string `json:"health,omitempty"`
	// +optional
	Revision string `json:"revision,omitempty"`
}

// EdgeStatus is the observed state. Ingest writes everything except
// conditions; the reconciler writes only conditions.
type EdgeStatus struct {
	// LastCheckinTime is stamped by central on receipt.
	// +optional
	LastCheckinTime *metav1.Time `json:"lastCheckinTime,omitempty"`
	// +optional
	Telemetry Telemetry `json:"telemetry,omitempty"`
	// Labels are what the edge reports about itself.
	// +optional
	Labels map[string]string `json:"labels,omitempty"`
	// +optional
	ArgoCD ArgoCDStatus `json:"argocd,omitempty"`
	// +optional
	Apps []App `json:"apps,omitempty"`
	// +optional
	AppsTotal int `json:"appsTotal"`
	// +optional
	AppsSynced int `json:"appsSynced"`
	// +optional
	AppsHealthy int `json:"appsHealthy"`
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// Edge is one autonomous ArgoCD edge cluster in the fleet.
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=edge
// +kubebuilder:printcolumn:name="ARGOCD",type=string,JSONPath=`.status.argocd.version`
// +kubebuilder:printcolumn:name="READY",type=string,JSONPath=`.status.conditions[?(@.type=="ArgoCDReady")].status`
// +kubebuilder:printcolumn:name="APPS",type=integer,JSONPath=`.status.appsTotal`
// +kubebuilder:printcolumn:name="SYNCED",type=integer,JSONPath=`.status.appsSynced`
// +kubebuilder:printcolumn:name="HEALTHY",type=integer,JSONPath=`.status.appsHealthy`
// +kubebuilder:printcolumn:name="REACHABLE",type=string,JSONPath=`.status.conditions[?(@.type=="Reachable")].status`
// +kubebuilder:printcolumn:name="LAST CHECKIN",type=date,JSONPath=`.status.lastCheckinTime`
type Edge struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   EdgeSpec   `json:"spec,omitempty"`
	Status EdgeStatus `json:"status,omitempty"`
}

// EdgeList is a list of Edges.
// +kubebuilder:object:root=true
type EdgeList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Edge `json:"items"`
}
