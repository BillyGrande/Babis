// Package v1alpha1 contains the fleet API types for babis.
//
// +kubebuilder:object:generate=true
// +groupName=fleet.babis.dev
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var (
	// GroupVersion is the group version used to register these objects.
	GroupVersion = schema.GroupVersion{Group: "fleet.babis.dev", Version: "v1alpha1"}

	// SchemeBuilder collects functions that add this group's types to a scheme.
	SchemeBuilder = runtime.NewSchemeBuilder(addKnownTypes)

	// AddToScheme adds the types in this group-version to the given scheme.
	AddToScheme = SchemeBuilder.AddToScheme
)

func addKnownTypes(s *runtime.Scheme) error {
	s.AddKnownTypes(GroupVersion, &Edge{}, &EdgeList{})
	metav1.AddToGroupVersion(s, GroupVersion)
	return nil
}
