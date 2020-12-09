package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NodeSpec defines the desired state of Node
type NodeSpec struct {
	// Join is the network to join
	Join string `json:"join"`
	// Client is the Ethereum 2.0 client to use
	Client Ethereum2Client `json:"client,omitempty"`
}

// NodeStatus defines the observed state of Node
type NodeStatus struct {
}

// Ethereum2Client is Ethereum 2.0 client
// +kubebuilder:validation:Enum=teku;prysm;lighthouse;nimbus
type Ethereum2Client string

const (
	// TekuClient is ConsenSys Pegasys Ethereum 2.0 client
	TekuClient Ethereum2Client = "teku"
	// PrysmClient is Prysmatic Labs Ethereum 2.0 client
	PrysmClient Ethereum2Client = "prysm"
	// LighthouseClient is SigmaPrime Ethereum 2.0 client
	LighthouseClient Ethereum2Client = "lighthouse"
	// NimbusClient is Status Ethereum 2.0 client
	NimbusClient Ethereum2Client = "nimbus"
)

// +kubebuilder:object:root=true

// Node is the Schema for the nodes API
type Node struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NodeSpec   `json:"spec,omitempty"`
	Status NodeStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// NodeList contains a list of Node
type NodeList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Node `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Node{}, &NodeList{})
}