// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package preview

import (
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// CreateBackup generates a new Backup resource for this ScheduledBackup.
// The backup name is generated with a timestamp suffix to ensure uniqueness.
func (sb *ScheduledBackup) CreateBackup(now time.Time) *Backup {
	// Generate backup name with timestamp
	backupName := fmt.Sprintf("%s-%s", sb.Name, now.Format("20060102-150405"))

	return &Backup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      backupName,
			Namespace: sb.Namespace,
			Labels: map[string]string{
				"scheduledbackup": sb.Name,
			},
		},
		Spec: BackupSpec{
			Cluster:       sb.Spec.Cluster,
			RetentionDays: sb.Spec.RetentionDays,
		},
	}
}
