// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"time"

	"github.com/robfig/cron"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	dbpreview "github.com/microsoft/documentdb-operator/api/preview"
)

// ScheduledBackupReconciler reconciles a ScheduledBackup object
type ScheduledBackupReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// Reconcile handles the reconciliation loop for ScheduledBackup resources.
func (r *ScheduledBackupReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Fetch the ScheduledBackup resource
	scheduledBackup := &dbpreview.ScheduledBackup{}
	if err := r.Get(ctx, req.NamespacedName, scheduledBackup); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Failed to get ScheduledBackup")
		return ctrl.Result{}, err
	}

	// Parse cron schedule
	schedule, err := cron.ParseStandard(scheduledBackup.Spec.Schedule)
	if err != nil {
		logger.Error(err, "Invalid cron schedule", "schedule", scheduledBackup.Spec.Schedule)
		return ctrl.Result{}, err
	}

	backupList := &dbpreview.BackupList{}
	if err := r.List(ctx, backupList, client.InNamespace(scheduledBackup.Namespace), client.MatchingFields{"spec.cluster": scheduledBackup.Spec.Cluster.Name}); err != nil {
		logger.Error(err, "Failed to list backups")
		// Requeue and try again shortly on list errors
		return ctrl.Result{RequeueAfter: time.Minute}, nil
	}

	if backupList.IsBackupRunning() {
		// If a backup is currently running, requeue after a short delay
		return ctrl.Result{RequeueAfter: time.Minute}, nil
	}

	// Calculate next schedule time
	nextScheduleTime := getNextScheduleTime(scheduledBackup, schedule, backupList)

	// If it's time to create a backup
	now := time.Now()
	if !now.Before(nextScheduleTime) {
		backup := scheduledBackup.CreateBackup(now)
		if err := r.Create(ctx, backup); err != nil {
			logger.Error(err, "Failed to create backup")
			return ctrl.Result{RequeueAfter: time.Minute}, nil
		}

		// Calculate next run time
		nextScheduleTime = schedule.Next(now)
	}

	// Requeue at next schedule time
	requeueAfter := time.Until(nextScheduleTime)
	if requeueAfter < 0 {
		requeueAfter = time.Minute
	}

	logger.Info("Next backup scheduled", "requeueAfter", requeueAfter, "nextTime", nextScheduleTime)
	return ctrl.Result{RequeueAfter: requeueAfter}, nil
}

func getNextScheduleTime(scheduledBackup *dbpreview.ScheduledBackup, schedule cron.Schedule, backupList *dbpreview.BackupList) time.Time {
	if backupList == nil || len(backupList.Items) == 0 {
		return schedule.Next(scheduledBackup.CreationTimestamp.Time)
	}

	lastBackupCreationTime := time.Time{}
	for _, backup := range backupList.Items {
		if backup.Labels["scheduledbackup"] == scheduledBackup.Name {
			if backup.CreationTimestamp.After(lastBackupCreationTime) {
				lastBackupCreationTime = backup.CreationTimestamp.Time
			}
		}
	}

	if lastBackupCreationTime.IsZero() {
		return schedule.Next(scheduledBackup.CreationTimestamp.Time)
	}

	return schedule.Next(lastBackupCreationTime)
}

// SetupWithManager sets up the controller with the Manager.
func (r *ScheduledBackupReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Register field index for spec.cluster so we can query Backups by cluster name
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &dbpreview.Backup{}, "spec.cluster", func(rawObj client.Object) []string {
		backup := rawObj.(*dbpreview.Backup)
		return []string{backup.Spec.Cluster.Name}
	}); err != nil {
		return err
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&dbpreview.ScheduledBackup{}).
		Complete(r)
}
