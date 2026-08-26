package cmd

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	cnpgv1 "github.com/cloudnative-pg/cloudnative-pg/api/v1"
	"github.com/spf13/cobra"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
	"sigs.k8s.io/yaml"
)

const documentDBKind = "DocumentDB"

type restoreOptions struct {
	backupName     string
	targetName     string
	sourceName     string
	namespace      string
	kubeContext    string
	dryRun         bool
	wait           bool
	waitTimeout    time.Duration
	pollInterval   time.Duration
	skipPhaseCheck bool
}

func newRestoreCommand() *cobra.Command {
	opts := &restoreOptions{namespace: defaultDocumentDBNamespace}

	cmd := &cobra.Command{
		Use:   "restore",
		Short: "Restore a backup into a new DocumentDB cluster",
		Long: `Restore creates a new DocumentDB resource that bootstraps from an existing Backup.

The new resource reuses the spec of the DocumentDB the backup was taken from, so
storage, resources, and version settings are carried over. Cluster replication
settings are dropped because the restored cluster starts as a standalone primary.`,
		Example: `  # Restore backup 'sample-20260101-020000' into a new cluster named 'sample-restored'
  kubectl documentdb restore --from-backup sample-20260101-020000 --name sample-restored

  # Preview the DocumentDB manifest without creating it
  kubectl documentdb restore --from-backup sample-20260101-020000 --name sample-restored --dry-run`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := opts.complete(); err != nil {
				return err
			}
			return opts.run(cmd.Context(), cmd)
		},
	}

	cmd.Flags().StringVar(&opts.backupName, "from-backup", opts.backupName, "Name of the Backup to restore from (required)")
	cmd.Flags().StringVar(&opts.targetName, "name", opts.targetName, "Name of the DocumentDB resource to create (required)")
	cmd.Flags().StringVar(&opts.sourceName, "source-documentdb", opts.sourceName, "DocumentDB whose spec is used as the template (defaults to the backup's source cluster)")
	cmd.Flags().StringVarP(&opts.namespace, "namespace", "n", opts.namespace, "Namespace containing the Backup and the new DocumentDB resource")
	cmd.Flags().StringVar(&opts.kubeContext, "context", opts.kubeContext, "Kubeconfig context to use (defaults to current context)")
	cmd.Flags().BoolVar(&opts.dryRun, "dry-run", false, "Print the DocumentDB manifest that would be created and exit")
	cmd.Flags().BoolVar(&opts.skipPhaseCheck, "allow-incomplete-backup", false, "Restore even if the backup has not completed (the restore will likely fail)")
	cmd.Flags().BoolVar(&opts.wait, "wait", false, "Wait for the restored cluster to report a healthy status")
	cmd.Flags().DurationVar(&opts.waitTimeout, "wait-timeout", 30*time.Minute, "Maximum time to wait when --wait is set")
	cmd.Flags().DurationVar(&opts.pollInterval, "poll-interval", 15*time.Second, "Polling interval when --wait is set")

	_ = cmd.MarkFlagRequired("from-backup")
	_ = cmd.MarkFlagRequired("name")

	return cmd
}

func (o *restoreOptions) complete() error {
	o.backupName = strings.TrimSpace(o.backupName)
	if o.backupName == "" {
		return errors.New("--from-backup is required")
	}

	o.targetName = strings.TrimSpace(o.targetName)
	if o.targetName == "" {
		return errors.New("--name is required")
	}

	o.sourceName = strings.TrimSpace(o.sourceName)
	o.kubeContext = strings.TrimSpace(o.kubeContext)

	o.namespace = strings.TrimSpace(o.namespace)
	if o.namespace == "" {
		o.namespace = defaultDocumentDBNamespace
	}

	if o.targetName == o.sourceName {
		return fmt.Errorf("--name %q must differ from --source-documentdb; restoring in place is not supported", o.targetName)
	}

	if o.waitTimeout <= 0 {
		o.waitTimeout = 30 * time.Minute
	}
	if o.pollInterval <= 0 {
		o.pollInterval = 15 * time.Second
	}

	return nil
}

func (o *restoreOptions) run(ctx context.Context, cmd *cobra.Command) error {
	config, contextName, err := loadConfigFunc(o.kubeContext)
	if err != nil {
		return fmt.Errorf("failed to load kubeconfig: %w", err)
	}
	if contextName == "" {
		contextName = "(current)"
	}

	dyn, err := dynamicClientForConfig(config)
	if err != nil {
		return fmt.Errorf("failed to create dynamic client: %w", err)
	}

	backupObj, err := dyn.Resource(backupGVR()).Namespace(o.namespace).Get(ctx, o.backupName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("Backup %q not found in namespace %q", o.backupName, o.namespace)
		}
		return fmt.Errorf("failed to get Backup %q in namespace %q: %w", o.backupName, o.namespace, err)
	}

	backup, err := toBackup(backupObj)
	if err != nil {
		return err
	}

	if !o.skipPhaseCheck && backup.Status.Phase != cnpgv1.BackupPhaseCompleted {
		return fmt.Errorf("Backup %s/%s is in phase %q, not %q; pass --allow-incomplete-backup to restore anyway",
			o.namespace, o.backupName, safeValue(string(backup.Status.Phase)), cnpgv1.BackupPhaseCompleted)
	}

	sourceName := o.sourceName
	if sourceName == "" {
		sourceName = backup.Spec.Cluster.Name
	}
	if sourceName == "" {
		return fmt.Errorf("Backup %s/%s does not record a source cluster; pass --source-documentdb", o.namespace, o.backupName)
	}
	if sourceName == o.targetName {
		return fmt.Errorf("--name %q must differ from the backup's source DocumentDB; restoring in place is not supported", o.targetName)
	}

	sourceObj, err := dyn.Resource(documentDBGVR()).Namespace(o.namespace).Get(ctx, sourceName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("source DocumentDB %q not found in namespace %q; pass --source-documentdb to use another cluster's spec as the template",
				sourceName, o.namespace)
		}
		return fmt.Errorf("failed to get source DocumentDB %q in namespace %q: %w", sourceName, o.namespace, err)
	}

	target, err := buildRestoreTarget(sourceObj, o.targetName, o.namespace, o.backupName)
	if err != nil {
		return err
	}

	if o.dryRun {
		manifest, err := yaml.Marshal(target.Object)
		if err != nil {
			return fmt.Errorf("failed to render DocumentDB manifest: %w", err)
		}
		fmt.Fprint(cmd.OutOrStdout(), string(manifest))
		return nil
	}

	if _, err := dyn.Resource(documentDBGVR()).Namespace(o.namespace).Create(ctx, target, metav1.CreateOptions{}); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("DocumentDB %q already exists in namespace %q; pick a different --name", o.targetName, o.namespace)
		}
		return fmt.Errorf("failed to create DocumentDB %q in namespace %q: %w", o.targetName, o.namespace, err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "DocumentDB %s/%s created from Backup %q (template %q, context %s).\n",
		o.namespace, o.targetName, o.backupName, sourceName, contextName)
	if backup.Status.SchemaVersion != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Backup schema version: %s. The restored cluster must run a binary at or above this version.\n", backup.Status.SchemaVersion)
	}

	if !o.wait {
		fmt.Fprintf(cmd.OutOrStdout(), "Track progress with: kubectl documentdb status --documentdb %s -n %s\n", o.targetName, o.namespace)
		return nil
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Waiting up to %s for the restored cluster to become healthy...\n", o.waitTimeout)
	if err := o.waitForRestore(ctx, dyn); err != nil {
		return err
	}

	fmt.Fprintf(cmd.OutOrStdout(), "DocumentDB %s/%s is healthy.\n", o.namespace, o.targetName)
	return nil
}

// buildRestoreTarget clones the source DocumentDB spec and points it at the
// backup to recover from. Working on the unstructured spec (rather than the
// typed one) keeps fields the plugin does not know about intact.
func buildRestoreTarget(source *unstructured.Unstructured, targetName, namespace, backupName string) (*unstructured.Unstructured, error) {
	sourceSpec, found, err := unstructured.NestedMap(source.Object, "spec")
	if err != nil {
		return nil, fmt.Errorf("failed to read spec of DocumentDB %q: %w", source.GetName(), err)
	}
	if !found {
		return nil, fmt.Errorf("DocumentDB %q has no spec to use as a restore template", source.GetName())
	}

	// The restored cluster comes up standalone; carrying over the source's
	// replication topology would point it at clusters it is not a member of.
	delete(sourceSpec, "clusterReplication")

	recovery := map[string]any{
		"backup": map[string]any{"name": backupName},
	}
	if err := unstructured.SetNestedMap(sourceSpec, recovery, "bootstrap", "recovery"); err != nil {
		return nil, fmt.Errorf("failed to set bootstrap.recovery: %w", err)
	}

	target := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": documentDBGVRGroup + "/" + documentDBGVRVersion,
		"kind":       documentDBKind,
		"metadata": map[string]any{
			"name":      targetName,
			"namespace": namespace,
		},
		"spec": sourceSpec,
	}}

	return target, nil
}

func (o *restoreOptions) waitForRestore(ctx context.Context, dyn dynamic.Interface) error {
	ctx, cancel := context.WithTimeout(ctx, o.waitTimeout)
	defer cancel()

	ticker := time.NewTicker(o.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out after %s waiting for DocumentDB %s/%s to become healthy", o.waitTimeout, o.namespace, o.targetName)
		case <-ticker.C:
			obj, err := dyn.Resource(documentDBGVR()).Namespace(o.namespace).Get(ctx, o.targetName, metav1.GetOptions{})
			if err != nil {
				if apierrors.IsNotFound(err) {
					continue
				}
				return fmt.Errorf("failed to get DocumentDB %s/%s: %w", o.namespace, o.targetName, err)
			}

			// A missing status means the operator has not observed the resource
			// yet, which isHealthyPhase optimistically reports as healthy. Wait
			// for a reported phase before declaring the restore done.
			phase, found, err := unstructured.NestedString(obj.Object, "status", "status")
			if err != nil || !found || strings.TrimSpace(phase) == "" {
				continue
			}
			if healthy, _ := isDocumentHealthy(obj); healthy {
				return nil
			}
		}
	}
}
