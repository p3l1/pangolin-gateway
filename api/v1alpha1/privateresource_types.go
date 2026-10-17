// SPDX-License-Identifier: AGPL-3.0-only

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Mode is the kind of destination a private resource exposes.
// +kubebuilder:validation:Enum=host;cidr;http
type Mode string

const (
	ModeHost Mode = "host"
	ModeCIDR Mode = "cidr"
	ModeHTTP Mode = "http"
)

// Scheme is how the site reaches an HTTP destination.
// +kubebuilder:validation:Enum=http;https
type Scheme string

const (
	SchemeHTTP  Scheme = "http"
	SchemeHTTPS Scheme = "https"
)

// Condition types written to status.conditions.
const (
	// ConditionAccepted is true when the spec is valid and no other object claims
	// its hostname or resource key.
	ConditionAccepted = "Accepted"

	// ConditionProgrammed is true once the resource is part of a successful apply.
	ConditionProgrammed = "Programmed"
)

// PrivateResourceSpec describes one entry under a blueprint's private-resources.
// Fields map one-to-one onto Pangolin's keys; which ones apply depends on mode.
type PrivateResourceSpec struct {
	// DisplayName is the name Pangolin shows. Defaults to "<namespace>/<name>".
	// +optional
	// +kubebuilder:validation:MaxLength=255
	DisplayName string `json:"displayName,omitempty"`

	// Mode is host (one address), cidr (a network) or http (a hostname proxied
	// over HTTPS to the destination).
	Mode Mode `json:"mode"`

	// Sites are the site niceIds the destination is reached through. Defaults to
	// the controller's --default-site.
	// +optional
	// +listType=set
	Sites []string `json:"sites,omitempty"`

	// Destination is a hostname or IP for host and http, a CIDR block for cidr.
	// +kubebuilder:validation:MinLength=1
	Destination string `json:"destination"`

	// TCPPorts and UDPPorts are port lists such as "80,443,8000-9000", or "*"
	// for every port. Unset means none. Only for host and cidr.
	// +optional
	TCPPorts string `json:"tcpPorts,omitempty"`
	// +optional
	UDPPorts string `json:"udpPorts,omitempty"`

	// DisableICMP blocks ping to the destination. Only for host and cidr.
	// +optional
	DisableICMP bool `json:"disableIcmp,omitempty"`

	// Alias is the DNS name clients resolve to the destination. Only for host,
	// and required there when the destination is a hostname rather than an IP.
	// +optional
	Alias string `json:"alias,omitempty"`

	// FullDomain is the hostname clients use. Required for http, and only there.
	// +optional
	FullDomain string `json:"fullDomain,omitempty"`

	// SSL serves FullDomain over HTTPS with a certificate. Defaults to true.
	// Only for http.
	// +optional
	SSL *bool `json:"ssl,omitempty"`

	// Scheme and DestinationPort describe how the site reaches the destination,
	// not how clients reach Pangolin. Only for http; Scheme defaults to http.
	// +optional
	Scheme Scheme `json:"scheme,omitempty"`
	// +optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	DestinationPort int32 `json:"destinationPort,omitempty"`

	// Roles, Users and Machines replace the resource's grants on every apply;
	// the organisation's admin role keeps access regardless. Users are usernames
	// or e-mail addresses, Machines are machine client niceIds.
	// +optional
	// +listType=set
	Roles []string `json:"roles,omitempty"`
	// +optional
	// +listType=set
	Users []string `json:"users,omitempty"`
	// +optional
	// +listType=set
	Machines []string `json:"machines,omitempty"`
}

// PrivateResourceStatus reports what the controller made of the spec.
type PrivateResourceStatus struct {
	// ResourceKey is the niceId the resource has in Pangolin, while it is published.
	// +optional
	ResourceKey string `json:"resourceKey,omitempty"`

	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// PrivateResource is a Pangolin private resource: reachable only through a
// connected Pangolin client, never from the internet.
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=ppr,categories=pangolin
// +kubebuilder:printcolumn:name="Mode",type=string,JSONPath=`.spec.mode`
// +kubebuilder:printcolumn:name="Destination",type=string,JSONPath=`.spec.destination`
// +kubebuilder:printcolumn:name="Key",type=string,JSONPath=`.status.resourceKey`
// +kubebuilder:printcolumn:name="Accepted",type=string,JSONPath=`.status.conditions[?(@.type=="Accepted")].status`
// +kubebuilder:printcolumn:name="Programmed",type=string,JSONPath=`.status.conditions[?(@.type=="Programmed")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type PrivateResource struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PrivateResourceSpec   `json:"spec"`
	Status PrivateResourceStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type PrivateResourceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PrivateResource `json:"items"`
}
