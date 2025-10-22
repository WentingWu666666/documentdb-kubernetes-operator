// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"fmt"
	"time"

	cnpgv1 "github.com/cloudnative-pg/cloudnative-pg/api/v1"
	snapshotv1 "github.com/kubernetes-csi/external-snapshotter/client/v8/apis/volumesnapshot/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	dbpreview "github.com/microsoft/documentdb-operator/api/preview"
)

// BackupReconciler reconciles a Backup object
type BackupReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// Reconcile handles the reconciliation loop for Backup resources.
func (r *BackupReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Fetch the Backup resource
	backup := &dbpreview.Backup{}
	if err := r.Get(ctx, req.NamespacedName, backup); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Failed to get Backup")
		return ctrl.Result{}, err
	}

	if backup.Status.ExpiredAt != nil && time.Now().After(backup.Status.ExpiredAt.Time) {
		// Backup has expired, delete it
		if err := r.Delete(ctx, backup); err != nil {
			logger.Error(err, "Failed to delete expired Backup", "backupName", backup.Name)
			return ctrl.Result{}, err
		}
		logger.Info("Successfully deleted expired Backup", "backupName", backup.Name)
		return ctrl.Result{}, nil
	}

	cluster := &dbpreview.DocumentDB{}
	clusterKey := client.ObjectKey{
		Name:      backup.Spec.Cluster.Name,
		Namespace: backup.Namespace,
	}
	if err := r.Get(ctx, clusterKey, cluster); err != nil {
		logger.Error(err, "Failed to get cluster for Backup", "clusterName", backup.Spec.Cluster.Name)
		return ctrl.Result{}, err
	}

	// Ensure VolumeSnapshotClass exists
	if err := r.ensureVolumeSnapshotClass(ctx, cluster.Spec.Environment); err != nil {
		backup.Status.Error = "Failed to ensure VolumeSnapshotClass: " + err.Error()
		backup.Status.Phase = cnpgv1.BackupPhaseFailed
		if updateErr := r.Status().Update(ctx, backup); updateErr != nil {
			return ctrl.Result{}, updateErr
		}
		return ctrl.Result{}, err
	}

	// Get or create the CNPG Backup
	cnpgBackup := &cnpgv1.Backup{}
	cnpgBackupKey := client.ObjectKey{
		Name:      backup.Name,
		Namespace: backup.Namespace,
	}

	err := r.Get(ctx, cnpgBackupKey, cnpgBackup)
	if err != nil {
		if apierrors.IsNotFound(err) {
			// Create CNPG Backup
			return r.createCNPGBackup(ctx, backup)
		}
		logger.Error(err, "Failed to get CNPG Backup")
		return ctrl.Result{}, err
	}

	// Update status based on CNPG Backup status
	return r.updateBackupStatus(ctx, backup, cnpgBackup, *cluster)
}

// ensureVolumeSnapshotClass creates a VolumeSnapshotClass based on the cloud environment
func (r *BackupReconciler) ensureVolumeSnapshotClass(ctx context.Context, environment string) error {
	logger := log.FromContext(ctx)

	// Check if any VolumeSnapshotClass exists
	vscList := &snapshotv1.VolumeSnapshotClassList{}
	if err := r.List(ctx, vscList); err != nil {
		logger.Error(err, "Failed to list VolumeSnapshotClasses")
		return err
	}

	for _, vsc := range vscList.Items {
		if val, ok := vsc.Annotations["snapshot.storage.kubernetes.io/is-default-class"]; ok && val == "true" {
			return nil
		}
	}
	logger.Info("No default VolumeSnapshotClass found, will create one")

	vsc := buildVolumeSnapshotClass(environment)
	if vsc == nil {
		err := fmt.Errorf("Please create a default VolumeSnapshotClass before creating backups")
		logger.Error(err, "Failed to build VolumeSnapshotClass", "environment", environment)
		return err
	}

	if err := r.Create(ctx, vsc); err != nil {
		logger.Error(err, "Failed to create VolumeSnapshotClass")
		return err
	}

	logger.Info("Successfully created VolumeSnapshotClass", "name", vsc.Name, "driver", vsc.Driver)
	return nil
}

// buildVolumeSnapshotClass builds a VolumeSnapshotClass based on cloud provider
func buildVolumeSnapshotClass(environment string) *snapshotv1.VolumeSnapshotClass {
	deletionPolicy := snapshotv1.VolumeSnapshotContentDelete

	var driver string
	var name string

	switch environment {
	case "aks":
		driver = "disk.csi.azure.com"
		name = "azure-disk-snapclass"
	default:
		// TODO: add support for other cloud providers
		return nil
	}

	return &snapshotv1.VolumeSnapshotClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Annotations: map[string]string{
				"snapshot.storage.kubernetes.io/is-default-class": "true",
			},
		},
		Driver:         driver,
		DeletionPolicy: deletionPolicy,
	}
}

// createCNPGBackup creates a new CNPG Backup resource
func (r *BackupReconciler) createCNPGBackup(ctx context.Context, backup *dbpreview.Backup) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	cnpgBackup := &cnpgv1.Backup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      backup.Name,
			Namespace: backup.Namespace,
		},
		Spec: cnpgv1.BackupSpec{
			Method: cnpgv1.BackupMethodVolumeSnapshot,
			Cluster: cnpgv1.LocalObjectReference{
				Name: backup.Spec.Cluster.Name,
			},
		},
	}

	// Set owner reference for garbage collection
	// This ensures that the CNPG Backup is deleted when the DocumentDB Backup is deleted.
	if err := controllerutil.SetControllerReference(backup, cnpgBackup, r.Scheme); err != nil {
		logger.Error(err, "Failed to set owner reference on CNPG Backup")
		return ctrl.Result{}, err
	}

	if err := r.Create(ctx, cnpgBackup); err != nil {
		logger.Error(err, "Failed to create CNPG Backup")
		return ctrl.Result{}, err
	}

	logger.Info("Successfully created CNPG Backup", "name", cnpgBackup.Name)

	// Requeue to check status
	return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
}

// updateBackupStatus updates the Backup status based on CNPG Backup status
func (r *BackupReconciler) updateBackupStatus(ctx context.Context, backup *dbpreview.Backup, cnpgBackup *cnpgv1.Backup, cluster dbpreview.DocumentDB) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	needsUpdate := false
	newPhase := cnpgBackup.Status.Phase

	if backup.Status.Phase != newPhase {
		backup.Status.Phase = newPhase
		needsUpdate = true
	}

	if !areTimesEqual(backup.Status.StartedAt, cnpgBackup.Status.StartedAt) {
		backup.Status.StartedAt = cnpgBackup.Status.StartedAt
		needsUpdate = true
	}

	if !areTimesEqual(backup.Status.StoppedAt, cnpgBackup.Status.StoppedAt) {
		backup.Status.StoppedAt = cnpgBackup.Status.StoppedAt
		needsUpdate = true
	}

	if backup.Status.Error != cnpgBackup.Status.Error {
		backup.Status.Error = cnpgBackup.Status.Error
		needsUpdate = true
	}

	if backup.Status.IsDone() {
		retentionHours := 0
		if backup.Spec.RetentionDays != nil {
			retentionHours = *backup.Spec.RetentionDays * 24
		} else if cluster.Spec.Backup != nil {
			retentionHours = cluster.Spec.Backup.RetentionDays * 24
		}

		retentionStart := backup.Status.StoppedAt
		if retentionStart == nil {
			retentionStart = &backup.CreationTimestamp
		}

		expirationTime := retentionStart.Add(time.Duration(retentionHours) * time.Hour)
		backup.Status.ExpiredAt = &metav1.Time{Time: expirationTime}
		needsUpdate = true
	}

	if needsUpdate {
		if err := r.Status().Update(ctx, backup); err != nil {
			logger.Error(err, "Failed to update Backup status")
			return ctrl.Result{}, err
		}
	}

	if backup.Status.IsDone() {
		requeueAfter := time.Until(backup.Status.ExpiredAt.Time)
		if requeueAfter < 0 {
			requeueAfter = time.Minute
		}
		return ctrl.Result{RequeueAfter: requeueAfter}, nil
	}

	// Backup is still in progress, requeue to check status again
	return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
}

// areTimesEqual compares two metav1.Time pointers for equality
func areTimesEqual(t1, t2 *metav1.Time) bool {
	if t1 == nil && t2 == nil {
		return true
	}
	if t1 == nil || t2 == nil {
		return false
	}
	return t1.Equal(t2)
}

// SetupWithManager sets up the controller with the Manager.
func (r *BackupReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Register VolumeSnapshotClass with the scheme
	if err := snapshotv1.AddToScheme(mgr.GetScheme()); err != nil {
		return err
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&dbpreview.Backup{}).
		Owns(&cnpgv1.Backup{}).
		Complete(r)
}
