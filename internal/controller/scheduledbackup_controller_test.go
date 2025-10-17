// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"time"

	cnpgv1 "github.com/cloudnative-pg/cloudnative-pg/api/v1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/robfig/cron"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	dbpreview "github.com/microsoft/documentdb-operator/api/preview"
)

var _ = Describe("ScheduledBackup Controller", func() {
	const (
		scheduledBackupName      = "test-scheduled-backup"
		scheduledBackupNamespace = "default"
		clusterName              = "test-cluster"
	)

	var (
		ctx    context.Context
		scheme *runtime.Scheme
	)

	BeforeEach(func() {
		ctx = context.Background()
		scheme = runtime.NewScheme()
		Expect(dbpreview.AddToScheme(scheme)).To(Succeed())
	})

	It("returns error for invalid cron schedule", func() {
		invalidSchedule := "invalid cron expression"
		scheduledBackup := &dbpreview.ScheduledBackup{
			ObjectMeta: metav1.ObjectMeta{
				Name:      scheduledBackupName,
				Namespace: scheduledBackupNamespace,
			},
			Spec: dbpreview.ScheduledBackupSpec{
				Schedule: invalidSchedule,
				Cluster: cnpgv1.LocalObjectReference{
					Name: clusterName,
				},
			},
		}

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(scheduledBackup).
			Build()

		reconciler := &ScheduledBackupReconciler{
			Client: fakeClient,
			Scheme: scheme,
		}

		result, err := reconciler.Reconcile(ctx, reconcile.Request{
			NamespacedName: types.NamespacedName{
				Name:      scheduledBackupName,
				Namespace: scheduledBackupNamespace,
			},
		})

		// expect err: invalid cron expression
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("invalid cron expression"))
		Expect(result.Requeue).To(BeFalse())
	})

	Describe("getNextScheduleTime", func() {
		scheduledBackup := dbpreview.ScheduledBackup{
			ObjectMeta: metav1.ObjectMeta{
				Name:              scheduledBackupName,
				Namespace:         scheduledBackupNamespace,
				CreationTimestamp: metav1.Time{Time: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)},
			},
			Spec: dbpreview.ScheduledBackupSpec{
				Schedule: "0 0 * * *",
				Cluster: cnpgv1.LocalObjectReference{
					Name: clusterName,
				},
			},
		}
		schedule, _ := cron.ParseStandard("0 0 * * *")

		It("returns next time based on ScheduledBackup creation time when no backups exist", func() {
			nextFromCreation := getNextScheduleTime(&scheduledBackup, schedule, &dbpreview.BackupList{Items: []dbpreview.Backup{}})
			Expect(nextFromCreation).To(Equal(schedule.Next(scheduledBackup.CreationTimestamp.Time)))

			nextFromCreation = getNextScheduleTime(&scheduledBackup, schedule, nil)
			Expect(nextFromCreation).To(Equal(schedule.Next(scheduledBackup.CreationTimestamp.Time)))
		})

		It("returns next time based on the last backup with matching label", func() {
			t1 := time.Date(2024, 12, 1, 1, 0, 0, 0, time.UTC)
			t2 := time.Date(2024, 12, 2, 1, 0, 0, 0, time.UTC)
			t3 := time.Date(2024, 12, 3, 1, 0, 0, 0, time.UTC)

			backupList := &dbpreview.BackupList{
				Items: []dbpreview.Backup{
					{
						ObjectMeta: metav1.ObjectMeta{
							CreationTimestamp: metav1.Time{Time: t1},
							Labels:            map[string]string{"scheduledbackup": scheduledBackupName},
						},
					},
					{
						ObjectMeta: metav1.ObjectMeta{
							CreationTimestamp: metav1.Time{Time: t2},
							Labels:            map[string]string{"scheduledbackup": scheduledBackupName},
						},
					},
					{
						ObjectMeta: metav1.ObjectMeta{
							CreationTimestamp: metav1.Time{Time: t3},
							Labels:            map[string]string{"scheduledbackup": "other-scheduled-backup"},
						},
					},
					{
						ObjectMeta: metav1.ObjectMeta{
							CreationTimestamp: metav1.Time{Time: t3},
						},
					},
				},
			}

			nextScheduleTime := getNextScheduleTime(&scheduledBackup, schedule, backupList)
			Expect(nextScheduleTime).To(Equal(schedule.Next(t2)))
		})

		It("returns next time based on ScheduledBackup creation time when no matching backups exist", func() {
			backupList := &dbpreview.BackupList{
				Items: []dbpreview.Backup{
					{
						ObjectMeta: metav1.ObjectMeta{
							CreationTimestamp: metav1.Time{Time: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)},
							Labels:            map[string]string{"scheduledbackup": "other-scheduled-backup"},
						},
					},
				},
			}

			nextScheduleTime := getNextScheduleTime(&scheduledBackup, schedule, backupList)
			Expect(nextScheduleTime).To(Equal(schedule.Next(scheduledBackup.CreationTimestamp.Time)))
		})
	})
})
