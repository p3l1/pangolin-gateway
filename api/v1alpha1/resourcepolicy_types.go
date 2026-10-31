// SPDX-License-Identifier: AGPL-3.0-only

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// PolicyRule is one access rule of a policy. Its order is its priority.
type PolicyRule struct {
	// Action is allow (skips authentication), deny, or pass (falls through to it).
	// +kubebuilder:validation:Enum=allow;deny;pass
	Action string `json:"action"`

	// Match is what Value is compared against: path, cidr, ip, country, asn or region.
	// +kubebuilder:validation:Enum=path;cidr;ip;country;asn;region
	Match string `json:"match"`

	// +kubebuilder:validation:MinLength=1
	Value string `json:"value"`
}

// ResourcePolicySpec describes one shared policy under a blueprint's
// public-policies. Fields map onto Pangolin's keys in camelCase.
type ResourcePolicySpec struct {
	// DisplayName is the name Pangolin shows. Defaults to "<namespace>/<name>".
	// +optional
	// +kubebuilder:validation:MaxLength=255
	DisplayName string `json:"displayName,omitempty"`

	// SSO requires a Pangolin login. Defaults to true.
	// +optional
	SSO *bool `json:"sso,omitempty"`

	// Roles and Users are granted access after sign-in. Roles must not name Admin.
	// +optional
	// +listType=set
	Roles []string `json:"roles,omitempty"`
	// +optional
	// +listType=set
	Users []string `json:"users,omitempty"`

	// EmailWhitelist holds e-mail addresses that may sign in with a one-time
	// code, and wildcard entries such as "*@example.com" for a whole domain.
	// +optional
	// +listType=set
	EmailWhitelist []string `json:"emailWhitelist,omitempty"`

	// Pincode is a six-digit PIN, which works beside SSO: either lets a visitor in.
	// +optional
	// +kubebuilder:validation:Pattern=`^[0-9]{6}$`
	Pincode string `json:"pincode,omitempty"`

	// Rules run before authentication, in list order.
	// +optional
	// +listType=atomic
	Rules []PolicyRule `json:"rules,omitempty"`
}

// ResourcePolicyStatus reports what the controller made of the spec.
type ResourcePolicyStatus struct {
	// PolicyKey is the niceId the policy has in Pangolin, while it is published.
	// +optional
	PolicyKey string `json:"policyKey,omitempty"`

	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// ResourcePolicy is a Pangolin shared resource policy that routes reference
// with the pangolin.p3l1.de/policy annotation. A licensed Pangolin feature.
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=prp,categories=pangolin
// +kubebuilder:printcolumn:name="Key",type=string,JSONPath=`.status.policyKey`
// +kubebuilder:printcolumn:name="Accepted",type=string,JSONPath=`.status.conditions[?(@.type=="Accepted")].status`
// +kubebuilder:printcolumn:name="Programmed",type=string,JSONPath=`.status.conditions[?(@.type=="Programmed")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type ResourcePolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ResourcePolicySpec   `json:"spec"`
	Status ResourcePolicyStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type ResourcePolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ResourcePolicy `json:"items"`
}
