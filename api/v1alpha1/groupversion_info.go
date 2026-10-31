// SPDX-License-Identifier: AGPL-3.0-only

// Package v1alpha1 holds the pangolin.p3l1.de API: resources this controller
// publishes to Pangolin that the Gateway API has no object for.
// +kubebuilder:object:generate=true
// +groupName=pangolin.p3l1.de
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var (
	GroupVersion = schema.GroupVersion{Group: "pangolin.p3l1.de", Version: "v1alpha1"}

	SchemeBuilder = runtime.NewSchemeBuilder(addKnownTypes)

	AddToScheme = SchemeBuilder.AddToScheme
)

func addKnownTypes(s *runtime.Scheme) error {
	s.AddKnownTypes(GroupVersion, &PrivateResource{}, &PrivateResourceList{},
		&ResourcePolicy{}, &ResourcePolicyList{})
	metav1.AddToGroupVersion(s, GroupVersion)
	return nil
}
