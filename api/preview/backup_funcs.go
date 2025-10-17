// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package preview

import (
	cnpgv1 "github.com/cloudnative-pg/cloudnative-pg/api/v1"
)

// IsDone returns true if the backup operation is completed or failed.
func (backupStatus *BackupStatus) IsDone() bool {
	return backupStatus.Phase == cnpgv1.BackupPhaseCompleted || backupStatus.Phase == cnpgv1.BackupPhaseFailed
}

// IsRunning returns true if the backup is currently in progress (not in a terminal state).
func (backupList *BackupList) IsBackupRunning() bool {
	for _, backup := range backupList.Items {
		if !backup.Status.IsDone() {
			return true
		}
	}
	return false
}
