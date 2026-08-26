package cmd

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	cnpgv1 "github.com/cloudnative-pg/cloudnative-pg/api/v1"
	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"

	"github.com/documentdb/documentdb-operator/api/preview"
)

// installFakeCluster points the command factories at an in-memory cluster
// seeded with objs and returns the client so tests can assert on writes.
func installFakeCluster(t *testing.T, objs ...*unstructured.Unstructured) dynamic.Interface {
	t.Helper()

	prevLoad := loadConfigFunc
	prevDynamic := dynamicClientForConfig
	t.Cleanup(func() {
		loadConfigFunc = prevLoad
		dynamicClientForConfig = prevDynamic
	})

	client := newFakeDynamicClient(objs...)
	loadConfigFunc = func(string) (*rest.Config, string, error) {
		return &rest.Config{Host: "test"}, "test-context", nil
	}
	dynamicClientForConfig = func(*rest.Config) (dynamic.Interface, error) {
		return client, nil
	}
	return client
}

func newBackupObject(t *testing.T, name, namespace, cluster string, created time.Time, labels map[string]string, status preview.BackupStatus) *unstructured.Unstructured {
	t.Helper()

	backup := &preview.Backup{
		TypeMeta: metav1.TypeMeta{
			APIVersion: documentDBGVRGroup + "/" + documentDBGVRVersion,
			Kind:       backupKind,
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			Namespace:         namespace,
			Labels:            labels,
			CreationTimestamp: metav1.NewTime(created),
		},
		Spec: preview.BackupSpec{
			Cluster: cnpgv1.LocalObjectReference{Name: cluster},
		},
		Status: status,
	}

	content, err := runtime.DefaultUnstructuredConverter.ToUnstructured(backup)
	if err != nil {
		t.Fatalf("failed to build Backup object: %v", err)
	}
	return &unstructured.Unstructured{Object: content}
}

func newScheduledBackupObject(t *testing.T, name, namespace, cluster, schedule string, retentionDays *int) *unstructured.Unstructured {
	t.Helper()

	scheduledBackup := &preview.ScheduledBackup{
		TypeMeta: metav1.TypeMeta{
			APIVersion: documentDBGVRGroup + "/" + documentDBGVRVersion,
			Kind:       scheduledBackupKind,
		},
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: preview.ScheduledBackupSpec{
			Cluster:       cnpgv1.LocalObjectReference{Name: cluster},
			Schedule:      schedule,
			RetentionDays: retentionDays,
		},
	}

	content, err := runtime.DefaultUnstructuredConverter.ToUnstructured(scheduledBackup)
	if err != nil {
		t.Fatalf("failed to build ScheduledBackup object: %v", err)
	}
	return &unstructured.Unstructured{Object: content}
}

func newTestCommand() (*cobra.Command, *bytes.Buffer) {
	cmd := &cobra.Command{}
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stdout)
	return cmd, &stdout
}

// ---------------------------------------------------------------------------
// backup create
// ---------------------------------------------------------------------------

func TestBackupCreateOptionsCompleteDefaults(t *testing.T) {
	prevNow := nowFunc
	defer func() { nowFunc = prevNow }()
	nowFunc = func() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) }

	o := &backupCreateOptions{documentDBName: "  sample ", namespace: "  "}
	if err := o.complete(); err != nil {
		t.Fatalf("complete returned error: %v", err)
	}
	if o.documentDBName != "sample" {
		t.Fatalf("expected documentDBName trimmed, got %q", o.documentDBName)
	}
	if o.namespace != defaultDocumentDBNamespace {
		t.Fatalf("expected default namespace, got %q", o.namespace)
	}
	if o.backupName != "sample-20260102-030405" {
		t.Fatalf("expected generated backup name, got %q", o.backupName)
	}
	if o.waitTimeout <= 0 || o.pollInterval <= 0 {
		t.Fatalf("expected positive wait settings, got %v/%v", o.waitTimeout, o.pollInterval)
	}
}

func TestBackupCreateOptionsCompleteValidates(t *testing.T) {
	t.Parallel()

	cases := map[string]backupCreateOptions{
		"missing documentdb": {},
		"negative retention": {documentDBName: "sample", retentionDays: -1},
	}
	for name, opts := range cases {
		opts := opts
		if err := opts.complete(); err == nil {
			t.Fatalf("expected error for case %q", name)
		}
	}
}

func TestBackupCreateRunCreatesBackup(t *testing.T) {
	namespace := defaultDocumentDBNamespace
	client := installFakeCluster(t, newDocument("sample", namespace, "cluster-a", "Ready"))

	cmd, out := newTestCommand()
	opts := &backupCreateOptions{
		documentDBName: "sample",
		backupName:     "sample-backup",
		namespace:      namespace,
		retentionDays:  7,
	}
	if err := opts.run(context.Background(), cmd); err != nil {
		t.Fatalf("run returned error: %v", err)
	}

	obj, err := client.Resource(backupGVR()).Namespace(namespace).Get(context.Background(), "sample-backup", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("expected Backup to be created: %v", err)
	}

	backup, err := toBackup(obj)
	if err != nil {
		t.Fatalf("failed to convert created Backup: %v", err)
	}
	if backup.Spec.Cluster.Name != "sample" {
		t.Fatalf("expected spec.cluster.name 'sample', got %q", backup.Spec.Cluster.Name)
	}
	if backup.Spec.RetentionDays == nil || *backup.Spec.RetentionDays != 7 {
		t.Fatalf("expected retentionDays 7, got %v", backup.Spec.RetentionDays)
	}
	if _, found := obj.Object["status"]; found {
		t.Fatal("expected the server-owned status to be stripped before create")
	}
	if !strings.Contains(out.String(), "sample-backup") {
		t.Fatalf("expected output to name the backup, got %q", out.String())
	}
}

func TestBackupCreateRunRequiresExistingDocumentDB(t *testing.T) {
	installFakeCluster(t)

	cmd, _ := newTestCommand()
	opts := &backupCreateOptions{documentDBName: "missing", backupName: "b", namespace: defaultDocumentDBNamespace}

	err := opts.run(context.Background(), cmd)
	if err == nil {
		t.Fatal("expected an error when the DocumentDB does not exist")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected a not-found error, got %v", err)
	}
}

func TestBackupCreateRunRejectsDuplicateName(t *testing.T) {
	namespace := defaultDocumentDBNamespace
	existing := newBackupObject(t, "sample-backup", namespace, "sample", time.Now(), nil, preview.BackupStatus{})
	installFakeCluster(t, newDocument("sample", namespace, "cluster-a", "Ready"), existing)

	cmd, _ := newTestCommand()
	opts := &backupCreateOptions{documentDBName: "sample", backupName: "sample-backup", namespace: namespace}

	err := opts.run(context.Background(), cmd)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("expected an already-exists error, got %v", err)
	}
}

func TestBackupCreateWaitReportsCompletion(t *testing.T) {
	namespace := defaultDocumentDBNamespace
	client := installFakeCluster(t, newDocument("sample", namespace, "cluster-a", "Ready"))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	go setBackupPhase(ctx, client, namespace, "sample-backup", cnpgv1.BackupPhaseCompleted, "")

	cmd, out := newTestCommand()
	opts := &backupCreateOptions{
		documentDBName: "sample",
		backupName:     "sample-backup",
		namespace:      namespace,
		wait:           true,
		waitTimeout:    5 * time.Second,
		pollInterval:   5 * time.Millisecond,
	}
	if err := opts.run(ctx, cmd); err != nil {
		t.Fatalf("run returned error: %v", err)
	}
	if !strings.Contains(out.String(), "completed") {
		t.Fatalf("expected completion message, got %q", out.String())
	}
}

func TestBackupCreateWaitReportsFailure(t *testing.T) {
	namespace := defaultDocumentDBNamespace
	client := installFakeCluster(t, newDocument("sample", namespace, "cluster-a", "Ready"))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	go setBackupPhase(ctx, client, namespace, "sample-backup", cnpgv1.BackupPhaseFailed, "snapshot class missing")

	cmd, _ := newTestCommand()
	opts := &backupCreateOptions{
		documentDBName: "sample",
		backupName:     "sample-backup",
		namespace:      namespace,
		wait:           true,
		waitTimeout:    5 * time.Second,
		pollInterval:   5 * time.Millisecond,
	}

	err := opts.run(ctx, cmd)
	if err == nil || !strings.Contains(err.Error(), "snapshot class missing") {
		t.Fatalf("expected the failure message to surface, got %v", err)
	}
}

// setBackupPhase waits for the Backup to appear and then writes a terminal phase,
// standing in for the operator's status updates.
func setBackupPhase(ctx context.Context, client dynamic.Interface, namespace, name, phase, message string) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		obj, err := client.Resource(backupGVR()).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			time.Sleep(time.Millisecond)
			continue
		}
		status := map[string]any{"phase": phase}
		if message != "" {
			status["message"] = message
		}
		if err := unstructured.SetNestedMap(obj.Object, status, "status"); err != nil {
			return
		}
		if _, err := client.Resource(backupGVR()).Namespace(namespace).Update(ctx, obj, metav1.UpdateOptions{}); err != nil {
			return
		}
		return
	}
}

// ---------------------------------------------------------------------------
// backup list
// ---------------------------------------------------------------------------

func TestBackupListOptionsCompleteRejectsUnknownStatus(t *testing.T) {
	t.Parallel()

	o := &backupListOptions{status: "bogus"}
	if err := o.complete(); err == nil {
		t.Fatal("expected an error for an unknown --status value")
	}

	o = &backupListOptions{}
	if err := o.complete(); err != nil {
		t.Fatalf("complete returned error: %v", err)
	}
	if o.statusFilter != backupStatusAll {
		t.Fatalf("expected default status filter 'all', got %q", o.statusFilter)
	}
	if o.namespace != defaultDocumentDBNamespace {
		t.Fatalf("expected default namespace, got %q", o.namespace)
	}
}

func TestBackupListRendersAndFilters(t *testing.T) {
	namespace := defaultDocumentDBNamespace
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	completed := newBackupObject(t, "sample-completed", namespace, "sample", base,
		map[string]string{scheduledBackupLabel: "nightly"},
		preview.BackupStatus{
			Phase:         cnpgv1.BackupPhaseCompleted,
			StartedAt:     ptrTime(base),
			StoppedAt:     ptrTime(base.Add(time.Minute)),
			SchemaVersion: "0.113-0",
		})
	running := newBackupObject(t, "sample-running", namespace, "sample", base.Add(time.Hour), nil,
		preview.BackupStatus{Phase: cnpgv1.BackupPhaseRunning, StartedAt: ptrTime(base.Add(time.Hour))})
	failed := newBackupObject(t, "sample-failed", namespace, "sample", base.Add(2*time.Hour), nil,
		preview.BackupStatus{Phase: cnpgv1.BackupPhaseFailed, Message: "boom"})
	other := newBackupObject(t, "other-completed", namespace, "other", base, nil,
		preview.BackupStatus{Phase: cnpgv1.BackupPhaseCompleted})

	installFakeCluster(t, completed, running, failed, other)

	t.Run("all backups for one documentdb", func(t *testing.T) {
		cmd, out := newTestCommand()
		opts := &backupListOptions{documentDBName: "sample", namespace: namespace, statusFilter: backupStatusAll}
		if err := opts.run(context.Background(), cmd); err != nil {
			t.Fatalf("run returned error: %v", err)
		}
		output := out.String()
		for _, want := range []string{"sample-completed", "sample-running", "sample-failed", "nightly", "0.113-0", "boom"} {
			if !strings.Contains(output, want) {
				t.Fatalf("expected output to contain %q, got:\n%s", want, output)
			}
		}
		if strings.Contains(output, "other-completed") {
			t.Fatalf("expected backups of other clusters to be filtered out, got:\n%s", output)
		}
		// Newest first.
		if strings.Index(output, "sample-failed") > strings.Index(output, "sample-completed") {
			t.Fatalf("expected newest backups first, got:\n%s", output)
		}
	})

	t.Run("running only", func(t *testing.T) {
		cmd, out := newTestCommand()
		opts := &backupListOptions{documentDBName: "sample", namespace: namespace, statusFilter: backupStatusRunning}
		if err := opts.run(context.Background(), cmd); err != nil {
			t.Fatalf("run returned error: %v", err)
		}
		output := out.String()
		if !strings.Contains(output, "sample-running") {
			t.Fatalf("expected running backup, got:\n%s", output)
		}
		if strings.Contains(output, "sample-completed") || strings.Contains(output, "sample-failed") {
			t.Fatalf("expected terminal backups to be filtered out, got:\n%s", output)
		}
	})

	t.Run("failed only", func(t *testing.T) {
		cmd, out := newTestCommand()
		opts := &backupListOptions{namespace: namespace, statusFilter: backupStatusFailed}
		if err := opts.run(context.Background(), cmd); err != nil {
			t.Fatalf("run returned error: %v", err)
		}
		output := out.String()
		if !strings.Contains(output, "sample-failed") {
			t.Fatalf("expected failed backup, got:\n%s", output)
		}
		if strings.Contains(output, "sample-running") || strings.Contains(output, "sample-completed") {
			t.Fatalf("expected only failed backups, got:\n%s", output)
		}
	})

	t.Run("by scheduled backup label", func(t *testing.T) {
		cmd, out := newTestCommand()
		opts := &backupListOptions{namespace: namespace, scheduledBackup: "nightly", statusFilter: backupStatusAll}
		if err := opts.run(context.Background(), cmd); err != nil {
			t.Fatalf("run returned error: %v", err)
		}
		output := out.String()
		if !strings.Contains(output, "sample-completed") {
			t.Fatalf("expected the scheduled backup, got:\n%s", output)
		}
		if strings.Contains(output, "sample-running") {
			t.Fatalf("expected non-scheduled backups to be filtered out, got:\n%s", output)
		}
	})

	t.Run("no matches", func(t *testing.T) {
		cmd, out := newTestCommand()
		opts := &backupListOptions{documentDBName: "nope", namespace: namespace, statusFilter: backupStatusAll}
		if err := opts.run(context.Background(), cmd); err != nil {
			t.Fatalf("run returned error: %v", err)
		}
		if !strings.Contains(out.String(), "No backups found") {
			t.Fatalf("expected an empty-result message, got:\n%s", out.String())
		}
	})
}

func TestMatchesBackupStatusTreatsSkippedSeparately(t *testing.T) {
	t.Parallel()

	skipped := &preview.Backup{Status: preview.BackupStatus{Phase: preview.BackupPhaseSkipped}}
	if !matchesBackupStatus(skipped, backupStatusSkipped) {
		t.Fatal("expected a skipped backup to match the skipped filter")
	}
	if matchesBackupStatus(skipped, backupStatusFailed) {
		t.Fatal("expected a skipped backup not to be reported as failed")
	}
	if matchesBackupStatus(skipped, backupStatusRunning) {
		t.Fatal("expected a skipped backup not to be reported as running")
	}
}

// ---------------------------------------------------------------------------
// backup schedule
// ---------------------------------------------------------------------------

func TestBackupScheduleCreateOptionsComplete(t *testing.T) {
	t.Parallel()

	o := &backupScheduleCreateOptions{documentDBName: " sample ", schedule: " 0 2 * * * "}
	if err := o.complete(); err != nil {
		t.Fatalf("complete returned error: %v", err)
	}
	if o.scheduleName != "sample-schedule" {
		t.Fatalf("expected generated schedule name, got %q", o.scheduleName)
	}
	if o.namespace != defaultDocumentDBNamespace {
		t.Fatalf("expected default namespace, got %q", o.namespace)
	}
}

func TestBackupScheduleCreateOptionsRejectsInvalidCron(t *testing.T) {
	t.Parallel()

	o := &backupScheduleCreateOptions{documentDBName: "sample", schedule: "every tuesday"}
	err := o.complete()
	if err == nil || !strings.Contains(err.Error(), "invalid --schedule") {
		t.Fatalf("expected an invalid schedule error, got %v", err)
	}
}

func TestBackupScheduleCreateRun(t *testing.T) {
	namespace := defaultDocumentDBNamespace
	client := installFakeCluster(t, newDocument("sample", namespace, "cluster-a", "Ready"))

	cmd, out := newTestCommand()
	opts := &backupScheduleCreateOptions{
		documentDBName: "sample",
		scheduleName:   "nightly",
		schedule:       "0 2 * * *",
		namespace:      namespace,
		retentionDays:  14,
	}
	if err := opts.run(context.Background(), cmd); err != nil {
		t.Fatalf("run returned error: %v", err)
	}

	obj, err := client.Resource(scheduledBackupGVR()).Namespace(namespace).Get(context.Background(), "nightly", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("expected ScheduledBackup to be created: %v", err)
	}

	var scheduledBackup preview.ScheduledBackup
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, &scheduledBackup); err != nil {
		t.Fatalf("failed to convert ScheduledBackup: %v", err)
	}
	if scheduledBackup.Spec.Schedule != "0 2 * * *" {
		t.Fatalf("unexpected schedule %q", scheduledBackup.Spec.Schedule)
	}
	if scheduledBackup.Spec.Cluster.Name != "sample" {
		t.Fatalf("unexpected cluster %q", scheduledBackup.Spec.Cluster.Name)
	}
	if scheduledBackup.Spec.RetentionDays == nil || *scheduledBackup.Spec.RetentionDays != 14 {
		t.Fatalf("expected retentionDays 14, got %v", scheduledBackup.Spec.RetentionDays)
	}
	if !strings.Contains(out.String(), "nightly") {
		t.Fatalf("expected output to name the schedule, got %q", out.String())
	}
}

func TestBackupScheduleCreateRunRequiresExistingDocumentDB(t *testing.T) {
	installFakeCluster(t)

	cmd, _ := newTestCommand()
	opts := &backupScheduleCreateOptions{
		documentDBName: "missing",
		scheduleName:   "nightly",
		schedule:       "0 2 * * *",
		namespace:      defaultDocumentDBNamespace,
	}

	err := opts.run(context.Background(), cmd)
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected a not-found error, got %v", err)
	}
}

func TestBackupScheduleListRenders(t *testing.T) {
	namespace := defaultDocumentDBNamespace
	retention := 14
	installFakeCluster(t,
		newScheduledBackupObject(t, "nightly", namespace, "sample", "0 2 * * *", &retention),
		newScheduledBackupObject(t, "weekly", namespace, "other", "0 3 * * 0", nil),
	)

	cmd, out := newTestCommand()
	opts := &backupScheduleListOptions{documentDBName: "sample", namespace: namespace}
	if err := opts.run(context.Background(), cmd); err != nil {
		t.Fatalf("run returned error: %v", err)
	}

	output := out.String()
	if !strings.Contains(output, "nightly") || !strings.Contains(output, "0 2 * * *") || !strings.Contains(output, "14") {
		t.Fatalf("expected the matching schedule in the output, got:\n%s", output)
	}
	if strings.Contains(output, "weekly") {
		t.Fatalf("expected schedules for other clusters to be filtered out, got:\n%s", output)
	}
}

func TestBackupScheduleListEmpty(t *testing.T) {
	installFakeCluster(t)

	cmd, out := newTestCommand()
	opts := &backupScheduleListOptions{namespace: defaultDocumentDBNamespace}
	if err := opts.run(context.Background(), cmd); err != nil {
		t.Fatalf("run returned error: %v", err)
	}
	if !strings.Contains(out.String(), "No backup schedules found") {
		t.Fatalf("expected an empty-result message, got:\n%s", out.String())
	}
}

func TestFormatTime(t *testing.T) {
	t.Parallel()

	if got := formatTime(nil); got != "-" {
		t.Fatalf("expected '-' for a nil time, got %q", got)
	}
	if got := formatTime(&metav1.Time{}); got != "-" {
		t.Fatalf("expected '-' for a zero time, got %q", got)
	}
	stamp := metav1.NewTime(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	if got := formatTime(&stamp); got != "2026-01-02T03:04:05Z" {
		t.Fatalf("unexpected formatted time %q", got)
	}
}

func ptrTime(t time.Time) *metav1.Time {
	stamp := metav1.NewTime(t)
	return &stamp
}
