// Package v1alpha1 contains the fleet.babis.dev/v1alpha1 API types.
// +kubebuilder:object:generate=true
// +groupName=fleet.babis.dev
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var (
	// GroupVersion is the group and version of the Edge API.
	GroupVersion = schema.GroupVersion{Group: "fleet.babis.dev", Version: "v1alpha1"}

	// SchemeBuilder registers the API types.
	SchemeBuilder = runtime.NewSchemeBuilder(addKnownTypes)

	// AddToScheme adds the API types to a scheme.
	AddToScheme = SchemeBuilder.AddToScheme
)

func addKnownTypes(s *runtime.Scheme) error {
	s.AddKnownTypes(GroupVersion, &Edge{}, &EdgeList{})
	metav1.AddToGroupVersion(s, GroupVersion)
	return nil
}
