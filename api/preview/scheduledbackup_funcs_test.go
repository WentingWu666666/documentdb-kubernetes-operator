// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package preview

import (
	"time"

	cnpgv1 "github.com/cloudnative-pg/cloudnative-pg/api/v1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var _ = Describe("ScheduledBackup", func() {
	Describe("CreateBackup", func() {
		It("creates a Backup with expected fields", func() {
			sb := &ScheduledBackup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "my-scheduled-backup",
					Namespace: "default",
				},
				Spec: ScheduledBackupSpec{
					Cluster: cnpgv1.LocalObjectReference{
						Name: "test-cluster",
					},
				},
			}

			fixedTime := time.Date(2025, 10, 20, 15, 30, 45, 0, time.UTC)
			backup := sb.CreateBackup(fixedTime)

			Expect(backup.Name).To(Equal("my-scheduled-backup-20251020-153045"))
			Expect(backup.Namespace).To(Equal("default"))
			Expect(backup.Labels).To(HaveKeyWithValue("scheduledbackup", "my-scheduled-backup"))
			Expect(backup.Spec.Cluster.Name).To(Equal("test-cluster"))
		})
	})
})
