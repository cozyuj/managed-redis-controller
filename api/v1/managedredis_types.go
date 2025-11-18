/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// ManagedRedisSpec defines the desired state of ManagedRedis
type ManagedRedisSpec struct {
	// INSERT ADDITIONAL SPEC FIELDS - desired state of cluster
	// Important: Run "make" to regenerate code after modifying this file
	// The following markers will use OpenAPI v3 schema to validate the value
	// More info: https://book.kubebuilder.io/reference/markers/crd-validation.html

	// foo is an example field of ManagedRedis. Edit managedredis_types.go to remove/update
	// +optional
	Version  string `json:"version,omitempty"`
	Replicas int32  `json:"replicas,omitempty"`

	Mode string `json:"mode,omitempty"`

	// Foo *string `json:"foo,omitempty"`
}

// ManagedRedisStatus defines the observed state of ManagedRedis.
type ManagedRedisStatus struct {
	Phase           string `json:"phase,omitempty"`
	PrimaryEndpoint string `json:"primaryEndpoint,omitempty"`
	ReplicaEndpoint string `json:"replicaEndpoint,omitempty"`

	Primary  *RedisNodeInfo  `json:"primary,omitempty"`
	Replicas []RedisNodeInfo `json:"replicas,omitempty"`

	ReadyReplicas int32 `json:"readyReplicas,omitempty"`
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	// For Kubernetes API conventions, see:
	// https://github.com/kubernetes/community/blob/master/contributors/devel/sig-architecture/api-conventions.md#typical-status-properties

	// conditions represent the current state of the ManagedRedis resource.
	// Each condition has a unique type and reflects the status of a specific aspect of the resource.
	//
	// Standard condition types include:
	// - "Available": the resource is fully functional
	// - "Progressing": the resource is being created or updated
	// - "Degraded": the resource failed to reach or maintain its desired state
	//
	// The status of each condition is one of True, False, or Unknown.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// ManagedRedis is the Schema for the managedredis API
type ManagedRedis struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty,omitzero"`

	// spec defines the desired state of ManagedRedis
	// +required
	Spec ManagedRedisSpec `json:"spec,omitempty"` //값이 비어있을 때 json 에서 필드 생략

	// status defines the observed state of ManagedRedis
	// +optional
	Status ManagedRedisStatus `json:"status,omitempty,omitzero"`
}

// +kubebuilder:object:root=true

// ManagedRedisList contains a list of ManagedRedis
type ManagedRedisList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ManagedRedis `json:"items"`
}

type RedisNodeInfo struct {
	Name     string `json:"name,omitempty"`
	Role     string `json:"role,omitempty"` // primary / replica
	PodIP    string `json:"podIP,omitempty"`
	NodeName string `json:"nodeName,omitempty"`
	Status   string `json:"status,omitempty"` // Pending / Running / Failed
}

func init() {
	SchemeBuilder.Register(&ManagedRedis{}, &ManagedRedisList{})
}
