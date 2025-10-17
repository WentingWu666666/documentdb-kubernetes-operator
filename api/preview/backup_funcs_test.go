// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package preview

import (
	cnpgv1 "github.com/cloudnative-pg/cloudnative-pg/api/v1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("BackupStatus", func() {
	Describe("IsDone", func() {
		It("returns true when phase is Completed", func() {
			status := &BackupStatus{
				Phase: cnpgv1.BackupPhaseCompleted,
			}
			Expect(status.IsDone()).To(BeTrue())
		})

		It("returns true when phase is Failed", func() {
			status := &BackupStatus{
				Phase: cnpgv1.BackupPhaseFailed,
			}
			Expect(status.IsDone()).To(BeTrue())
		})

		It("returns false when phase is Running", func() {
			status := &BackupStatus{
				Phase: cnpgv1.BackupPhaseRunning,
			}
			Expect(status.IsDone()).To(BeFalse())
		})

		It("returns false when phase is empty", func() {
			status := &BackupStatus{
				Phase: cnpgv1.BackupPhase(""),
			}
			Expect(status.IsDone()).To(BeFalse())
		})
	})
})

var _ = Describe("BackupList", func() {
	Describe("IsBackupRunning", func() {
		It("returns false when all backups are in terminal phases", func() {
			backupList := &BackupList{
				Items: []Backup{
					{
						Status: BackupStatus{
							Phase: cnpgv1.BackupPhaseCompleted,
						},
					},
					{
						Status: BackupStatus{
							Phase: cnpgv1.BackupPhaseFailed,
						},
					},
				},
			}
			Expect(backupList.IsBackupRunning()).To(BeFalse())
		})

		It("returns false when backup list is empty", func() {
			backupList := &BackupList{
				Items: []Backup{},
			}
			Expect(backupList.IsBackupRunning()).To(BeFalse())
		})

		It("returns true when at least one backup is running among completed backups", func() {
			backupList := &BackupList{
				Items: []Backup{
					{
						Status: BackupStatus{
							Phase: cnpgv1.BackupPhaseCompleted,
						},
					},
					{
						Status: BackupStatus{
							Phase: cnpgv1.BackupPhaseRunning,
						},
					},
					{
						Status: BackupStatus{
							Phase: cnpgv1.BackupPhaseFailed,
						},
					},
				},
			}
			Expect(backupList.IsBackupRunning()).To(BeTrue())
		})
	})
})
