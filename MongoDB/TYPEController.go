/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// OperatorSpec defines the desired state of Operator
type OperatorSpec struct {
	// ConfigReplicas is the number of config server replicas
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=7
	// +kubebuilder:default=3
	// +optional
	ConfigReplicas int `json:"configReplicas,omitempty"`

	// Shards is the number of shard replica sets
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=1024
	// +kubebuilder:default=2
	// +optional
	Shards int `json:"shards,omitempty"`

	// Replicas is the number of replicas per shard
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=50
	// +kubebuilder:default=3
	// +optional
	Replicas int `json:"replicas,omitempty"`

	// MongoImage is the MongoDB Docker image to use
	// +kubebuilder:default="mongo:7.0"
	// +optional
	MongoImage string `json:"mongoImage,omitempty"`

	// StorageSize is the storage size for each MongoDB instance
	// +kubebuilder:default="10Gi"
	// +optional
	StorageSize string `json:"storageSize,omitempty"`

	// StorageClassName is the storage class to use for persistent volumes
	// +optional
	StorageClassName *string `json:"storageClassName,omitempty"`
}

// OperatorStatus defines the observed state of Operator.
type OperatorStatus struct {
	// conditions represent the current state of the Operator resource.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// Phase represents the current phase of the MongoDB cluster deployment
	// +kubebuilder:validation:Enum=Pending;Progressing;Ready;Failed
	// +optional
	Phase string `json:"phase,omitempty"`

	// ConfigReady indicates if the config servers are ready
	// +optional
	ConfigReady bool `json:"configReady,omitempty"`

	// ShardsReady indicates how many shards are ready
	// +optional
	ShardsReady int `json:"shardsReady,omitempty"`

	// MongosReady indicates if the mongos router is ready
	// +optional
	MongosReady bool `json:"mongosReady,omitempty"`

	// ObservedGeneration is the most recent generation observed by the controller
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Message provides additional information about the current status
	// +optional
	Message string `json:"message,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Config Ready",type="boolean",JSONPath=".status.configReady"
// +kubebuilder:printcolumn:name="Shards Ready",type="integer",JSONPath=".status.shardsReady"
// +kubebuilder:printcolumn:name="Mongos Ready",type="boolean",JSONPath=".status.mongosReady"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// Operator is the Schema for the operators API
type Operator struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   OperatorSpec   `json:"spec,omitempty"`
	Status OperatorStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// OperatorList contains a list of Operator
type OperatorList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Operator `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Operator{}, &OperatorList{})
}
