package cmd

import (
	"context"
	"strings"
	"testing"
	"time"

	cnpgv1 "github.com/cloudnative-pg/cloudnative-pg/api/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	"github.com/documentdb/documentdb-operator/api/preview"
)

func newRestoreSourceDocument(name, namespace string) *unstructured.Unstructured {
	doc := &unstructured.Unstructured{Object: map[string]any{
		"spec": map[string]any{
			"nodeCount":        int64(1),
			"instancesPerNode": int64(3),
			"documentDBImage":  "ghcr.io/documentdb/documentdb:0.113.0",
			"resource": map[string]any{
				"pvcSize": "10Gi",
			},
			// A field the plugin knows nothing about; it must survive the clone.
			"someFutureField": "keep-me",
			"clusterReplication": map[string]any{
				"primary":     "cluster-a",
				"clusterList": []any{map[string]any{"name": "cluster-a"}},
			},
		},
		"status": map[string]any{"status": "Ready"},
	}}
	doc.SetGroupVersionKind(schema.GroupVersionKind{Group: documentDBGVRGroup, Version: documentDBGVRVersion, Kind: documentDBKind})
	doc.SetName(name)
	doc.SetNamespace(namespace)
	return doc
}

func TestRestoreOptionsCompleteValidates(t *testing.T) {
	t.Parallel()

	cases := map[string]restoreOptions{
		"missing backup":   {targetName: "restored"},
		"missing target":   {backupName: "backup"},
		"restore in place": {backupName: "backup", targetName: "sample", sourceName: "sample"},
	}
	for name, opts := range cases {
		opts := opts
		if err := opts.complete(); err == nil {
			t.Fatalf("expected error for case %q", name)
		}
	}

	o := &restoreOptions{backupName: " backup ", targetName: " restored ", namespace: "  "}
	if err := o.complete(); err != nil {
		t.Fatalf("complete returned error: %v", err)
	}
	if o.backupName != "backup" || o.targetName != "restored" {
		t.Fatalf("expected trimmed names, got %q/%q", o.backupName, o.targetName)
	}
	if o.namespace != defaultDocumentDBNamespace {
		t.Fatalf("expected default namespace, got %q", o.namespace)
	}
	if o.waitTimeout <= 0 || o.pollInterval <= 0 {
		t.Fatalf("expected positive wait settings, got %v/%v", o.waitTimeout, o.pollInterval)
	}
}

func TestBuildRestoreTargetClonesSpec(t *testing.T) {
	t.Parallel()

	source := newRestoreSourceDocument("sample", defaultDocumentDBNamespace)

	target, err := buildRestoreTarget(source, "sample-restored", defaultDocumentDBNamespace, "sample-backup")
	if err != nil {
		t.Fatalf("buildRestoreTarget returned error: %v", err)
	}

	if target.GetName() != "sample-restored" || target.GetNamespace() != defaultDocumentDBNamespace {
		t.Fatalf("unexpected target metadata %s/%s", target.GetNamespace(), target.GetName())
	}
	if target.GetKind() != documentDBKind {
		t.Fatalf("unexpected kind %q", target.GetKind())
	}

	backupName, found, err := unstructured.NestedString(target.Object, "spec", "bootstrap", "recovery", "backup", "name")
	if err != nil || !found {
		t.Fatalf("expected spec.bootstrap.recovery.backup.name to be set (found=%v, err=%v)", found, err)
	}
	if backupName != "sample-backup" {
		t.Fatalf("expected recovery backup 'sample-backup', got %q", backupName)
	}

	if _, found, _ := unstructured.NestedMap(target.Object, "spec", "clusterReplication"); found {
		t.Fatal("expected clusterReplication to be dropped from the restored spec")
	}

	if _, found, _ := unstructured.NestedMap(target.Object, "status"); found {
		t.Fatal("expected the source status not to be copied")
	}

	preserved, _, _ := unstructured.NestedString(target.Object, "spec", "someFutureField")
	if preserved != "keep-me" {
		t.Fatalf("expected unknown spec fields to be preserved, got %q", preserved)
	}
	image, _, _ := unstructured.NestedString(target.Object, "spec", "documentDBImage")
	if image != "ghcr.io/documentdb/documentdb:0.113.0" {
		t.Fatalf("expected the source image to be carried over, got %q", image)
	}

	// The source must not be mutated in the process.
	if _, found, _ := unstructured.NestedMap(source.Object, "spec", "clusterReplication"); !found {
		t.Fatal("expected the source document to be left untouched")
	}
}

func TestRestoreRunRejectsIncompleteBackup(t *testing.T) {
	namespace := defaultDocumentDBNamespace
	backup := newBackupObject(t, "sample-backup", namespace, "sample", time.Now(), nil,
		preview.BackupStatus{Phase: cnpgv1.BackupPhaseRunning})
	installFakeCluster(t, newRestoreSourceDocument("sample", namespace), backup)

	cmd, _ := newTestCommand()
	opts := &restoreOptions{backupName: "sample-backup", targetName: "sample-restored", namespace: namespace}

	err := opts.run(context.Background(), cmd)
	if err == nil || !strings.Contains(err.Error(), "allow-incomplete-backup") {
		t.Fatalf("expected a phase-guard error, got %v", err)
	}
}

func TestRestoreRunRejectsMissingBackup(t *testing.T) {
	installFakeCluster(t)

	cmd, _ := newTestCommand()
	opts := &restoreOptions{backupName: "nope", targetName: "restored", namespace: defaultDocumentDBNamespace}

	err := opts.run(context.Background(), cmd)
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected a not-found error, got %v", err)
	}
}

func TestRestoreRunRejectsMissingSourceDocumentDB(t *testing.T) {
	namespace := defaultDocumentDBNamespace
	backup := newBackupObject(t, "sample-backup", namespace, "sample", time.Now(), nil,
		preview.BackupStatus{Phase: cnpgv1.BackupPhaseCompleted})
	installFakeCluster(t, backup)

	cmd, _ := newTestCommand()
	opts := &restoreOptions{backupName: "sample-backup", targetName: "sample-restored", namespace: namespace}

	err := opts.run(context.Background(), cmd)
	if err == nil || !strings.Contains(err.Error(), "--source-documentdb") {
		t.Fatalf("expected guidance about --source-documentdb, got %v", err)
	}
}

func TestRestoreRunCreatesDocumentDB(t *testing.T) {
	namespace := defaultDocumentDBNamespace
	backup := newBackupObject(t, "sample-backup", namespace, "sample", time.Now(), nil,
		preview.BackupStatus{Phase: cnpgv1.BackupPhaseCompleted, SchemaVersion: "0.113-0"})
	client := installFakeCluster(t, newRestoreSourceDocument("sample", namespace), backup)

	cmd, out := newTestCommand()
	opts := &restoreOptions{backupName: "sample-backup", targetName: "sample-restored", namespace: namespace}
	if err := opts.run(context.Background(), cmd); err != nil {
		t.Fatalf("run returned error: %v", err)
	}

	created, err := client.Resource(documentDBGVR()).Namespace(namespace).Get(context.Background(), "sample-restored", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("expected the restored DocumentDB to be created: %v", err)
	}
	backupRef, _, _ := unstructured.NestedString(created.Object, "spec", "bootstrap", "recovery", "backup", "name")
	if backupRef != "sample-backup" {
		t.Fatalf("expected the restored cluster to bootstrap from the backup, got %q", backupRef)
	}

	output := out.String()
	if !strings.Contains(output, "sample-restored") {
		t.Fatalf("expected output to name the new cluster, got:\n%s", output)
	}
	if !strings.Contains(output, "0.113-0") {
		t.Fatalf("expected the backup schema version to be surfaced, got:\n%s", output)
	}
}

func TestRestoreRunDryRunDoesNotCreate(t *testing.T) {
	namespace := defaultDocumentDBNamespace
	backup := newBackupObject(t, "sample-backup", namespace, "sample", time.Now(), nil,
		preview.BackupStatus{Phase: cnpgv1.BackupPhaseCompleted})
	client := installFakeCluster(t, newRestoreSourceDocument("sample", namespace), backup)

	cmd, out := newTestCommand()
	opts := &restoreOptions{backupName: "sample-backup", targetName: "sample-restored", namespace: namespace, dryRun: true}
	if err := opts.run(context.Background(), cmd); err != nil {
		t.Fatalf("run returned error: %v", err)
	}

	if _, err := client.Resource(documentDBGVR()).Namespace(namespace).Get(context.Background(), "sample-restored", metav1.GetOptions{}); err == nil {
		t.Fatal("expected --dry-run not to create the DocumentDB")
	}

	output := out.String()
	for _, want := range []string{"kind: DocumentDB", "name: sample-restored", "bootstrap:", "sample-backup"} {
		if !strings.Contains(output, want) {
			t.Fatalf("expected the rendered manifest to contain %q, got:\n%s", want, output)
		}
	}
	if strings.Contains(output, "clusterReplication") {
		t.Fatalf("expected clusterReplication to be dropped from the manifest, got:\n%s", output)
	}
}

func TestRestoreRunRejectsExistingTarget(t *testing.T) {
	namespace := defaultDocumentDBNamespace
	backup := newBackupObject(t, "sample-backup", namespace, "sample", time.Now(), nil,
		preview.BackupStatus{Phase: cnpgv1.BackupPhaseCompleted})
	installFakeCluster(t,
		newRestoreSourceDocument("sample", namespace),
		newRestoreSourceDocument("sample-restored", namespace),
		backup,
	)

	cmd, _ := newTestCommand()
	opts := &restoreOptions{backupName: "sample-backup", targetName: "sample-restored", namespace: namespace}

	err := opts.run(context.Background(), cmd)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("expected an already-exists error, got %v", err)
	}
}

func TestRestoreWaitReportsHealthyCluster(t *testing.T) {
	namespace := defaultDocumentDBNamespace
	backup := newBackupObject(t, "sample-backup", namespace, "sample", time.Now(), nil,
		preview.BackupStatus{Phase: cnpgv1.BackupPhaseCompleted})
	client := installFakeCluster(t, newRestoreSourceDocument("sample", namespace), backup)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	go setDocumentPhase(ctx, client, namespace, "sample-restored", "Healthy")

	cmd, out := newTestCommand()
	opts := &restoreOptions{
		backupName:   "sample-backup",
		targetName:   "sample-restored",
		namespace:    namespace,
		wait:         true,
		waitTimeout:  5 * time.Second,
		pollInterval: 5 * time.Millisecond,
	}
	if err := opts.run(ctx, cmd); err != nil {
		t.Fatalf("run returned error: %v", err)
	}
	if !strings.Contains(out.String(), "is healthy") {
		t.Fatalf("expected a healthy message, got:\n%s", out.String())
	}
}

// setDocumentPhase stands in for the operator writing status.status once the
// restored cluster settles.
func setDocumentPhase(ctx context.Context, client dynamic.Interface, namespace, name, phase string) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		obj, err := client.Resource(documentDBGVR()).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			time.Sleep(time.Millisecond)
			continue
		}
		if err := unstructured.SetNestedField(obj.Object, phase, "status", "status"); err != nil {
			return
		}
		if _, err := client.Resource(documentDBGVR()).Namespace(namespace).Update(ctx, obj, metav1.UpdateOptions{}); err != nil {
			return
		}
		return
	}
}
