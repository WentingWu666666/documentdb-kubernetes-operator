// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package preview

import (
	cnpgv1 "github.com/cloudnative-pg/cloudnative-pg/api/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ScheduledBackupSpec defines the desired state of ScheduledBackup
type ScheduledBackupSpec struct {
	// Cluster specifies the DocumentDB cluster to backup.
	// The cluster must exist in the same namespace as the ScheduledBackup resource.
	// +kubebuilder:validation:Required
	Cluster cnpgv1.LocalObjectReference `json:"cluster"`

	// Schedule defines when backups should be created using cron expression format.
	// +kubebuilder:validation:Required
	Schedule string `json:"schedule"`

	// RetentionDays specifies how many days the backups should be retained.
	// If not specified, the default retention period from the cluster's backup policy is used.
	// +optional
	RetentionDays *int `json:"retentionDays,omitempty"`
}

// ScheduledBackupStatus defines the observed state of ScheduledBackup
type ScheduledBackupStatus struct {
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Cluster",type="string",JSONPath=".spec.cluster.name"
// +kubebuilder:printcolumn:name="Schedule",type="string",JSONPath=".spec.schedule"
// +kubebuilder:printcolumn:name="Retention Days",type="integer",JSONPath=".spec.retentionDays"
type ScheduledBackup struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata"`
	Spec              ScheduledBackupSpec   `json:"spec"`
	Status            ScheduledBackupStatus `json:"status,omitempty"`
}

// ScheduledBackupList contains a list of ScheduledBackup resources
// +kubebuilder:object:root=true
type ScheduledBackupList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ScheduledBackup `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ScheduledBackup{}, &ScheduledBackupList{})
}
