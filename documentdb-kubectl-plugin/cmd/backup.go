package cmd

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	cnpgv1 "github.com/cloudnative-pg/cloudnative-pg/api/v1"
	"github.com/robfig/cron"
	"github.com/spf13/cobra"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic"

	"github.com/documentdb/documentdb-operator/api/preview"
)

const (
	backupKind          = "Backup"
	scheduledBackupKind = "ScheduledBackup"

	// scheduledBackupLabel is the label the operator stamps on Backup resources
	// it creates from a ScheduledBackup. See ScheduledBackup.CreateBackup.
	scheduledBackupLabel = "scheduledbackup"

	// backupNameTimestampLayout matches the suffix the operator uses when it
	// generates Backup names for a ScheduledBackup, so manually created and
	// scheduled backups sort and read consistently.
	backupNameTimestampLayout = "20060102-150405"
)

// nowFunc is overridable in tests so generated resource names are deterministic.
var nowFunc = time.Now

// backupStatusFilter enumerates the accepted values of `backup list --status`.
type backupStatusFilter string

const (
	backupStatusAll       backupStatusFilter = "all"
	backupStatusRunning   backupStatusFilter = "running"
	backupStatusCompleted backupStatusFilter = "completed"
	backupStatusFailed    backupStatusFilter = "failed"
	backupStatusSkipped   backupStatusFilter = "skipped"
)

func newBackupCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "backup",
		Short: "Create and inspect DocumentDB backups",
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}

	cmd.AddCommand(newBackupCreateCommand())
	cmd.AddCommand(newBackupListCommand())
	cmd.AddCommand(newBackupScheduleCommand())

	return cmd
}

// ---------------------------------------------------------------------------
// backup create
// ---------------------------------------------------------------------------

type backupCreateOptions struct {
	documentDBName string
	backupName     string
	namespace      string
	kubeContext    string
	retentionDays  int
	wait           bool
	waitTimeout    time.Duration
	pollInterval   time.Duration
}

func newBackupCreateCommand() *cobra.Command {
	opts := &backupCreateOptions{namespace: defaultDocumentDBNamespace}

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Start an on-demand backup of a DocumentDB cluster",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := opts.complete(); err != nil {
				return err
			}
			return opts.run(cmd.Context(), cmd)
		},
	}

	cmd.Flags().StringVar(&opts.documentDBName, "documentdb", opts.documentDBName, "Name of the DocumentDB resource to back up")
	cmd.Flags().StringVar(&opts.backupName, "name", opts.backupName, "Name of the Backup resource to create (defaults to <documentdb>-<timestamp>)")
	cmd.Flags().StringVarP(&opts.namespace, "namespace", "n", opts.namespace, "Namespace containing the DocumentDB resource")
	cmd.Flags().StringVar(&opts.kubeContext, "context", opts.kubeContext, "Kubeconfig context to use (defaults to current context)")
	cmd.Flags().IntVar(&opts.retentionDays, "retention-days", 0, "Days to retain this backup (defaults to the cluster's backup retention policy)")
	cmd.Flags().BoolVar(&opts.wait, "wait", false, "Wait for the backup to reach a terminal phase before returning")
	cmd.Flags().DurationVar(&opts.waitTimeout, "wait-timeout", 30*time.Minute, "Maximum time to wait when --wait is set")
	cmd.Flags().DurationVar(&opts.pollInterval, "poll-interval", 10*time.Second, "Polling interval when --wait is set")

	_ = cmd.MarkFlagRequired("documentdb")

	return cmd
}

func (o *backupCreateOptions) complete() error {
	o.documentDBName = strings.TrimSpace(o.documentDBName)
	if o.documentDBName == "" {
		return errors.New("--documentdb is required")
	}

	o.namespace = strings.TrimSpace(o.namespace)
	if o.namespace == "" {
		o.namespace = defaultDocumentDBNamespace
	}

	o.kubeContext = strings.TrimSpace(o.kubeContext)

	o.backupName = strings.TrimSpace(o.backupName)
	if o.backupName == "" {
		o.backupName = fmt.Sprintf("%s-%s", o.documentDBName, nowFunc().UTC().Format(backupNameTimestampLayout))
	}

	if o.retentionDays < 0 {
		return fmt.Errorf("--retention-days must be greater than zero, got %d", o.retentionDays)
	}

	if o.waitTimeout <= 0 {
		o.waitTimeout = 30 * time.Minute
	}
	if o.pollInterval <= 0 {
		o.pollInterval = 10 * time.Second
	}

	return nil
}

func (o *backupCreateOptions) run(ctx context.Context, cmd *cobra.Command) error {
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

	// Fail fast with a clear message instead of leaving an orphaned Backup that
	// the operator can only reject once it reconciles.
	if _, err := dyn.Resource(documentDBGVR()).Namespace(o.namespace).Get(ctx, o.documentDBName, metav1.GetOptions{}); err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("DocumentDB %q not found in namespace %q", o.documentDBName, o.namespace)
		}
		return fmt.Errorf("failed to get DocumentDB %q in namespace %q: %w", o.documentDBName, o.namespace, err)
	}

	backup := &preview.Backup{
		TypeMeta: metav1.TypeMeta{
			APIVersion: documentDBGVRGroup + "/" + documentDBGVRVersion,
			Kind:       backupKind,
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      o.backupName,
			Namespace: o.namespace,
		},
		Spec: preview.BackupSpec{
			Cluster: cnpgv1.LocalObjectReference{Name: o.documentDBName},
		},
	}
	if o.retentionDays > 0 {
		retention := o.retentionDays
		backup.Spec.RetentionDays = &retention
	}

	obj, err := toUnstructured(backup)
	if err != nil {
		return err
	}

	if _, err := dyn.Resource(backupGVR()).Namespace(o.namespace).Create(ctx, obj, metav1.CreateOptions{}); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("Backup %q already exists in namespace %q; pass --name to choose a different name", o.backupName, o.namespace)
		}
		return fmt.Errorf("failed to create Backup %q in namespace %q: %w", o.backupName, o.namespace, err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Backup %s/%s requested for DocumentDB %q (context %s).\n", o.namespace, o.backupName, o.documentDBName, contextName)

	if !o.wait {
		fmt.Fprintf(cmd.OutOrStdout(), "Track progress with: kubectl documentdb backup list --documentdb %s -n %s\n", o.documentDBName, o.namespace)
		return nil
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Waiting up to %s for the backup to finish...\n", o.waitTimeout)
	return o.waitForBackup(ctx, cmd, dyn)
}

func (o *backupCreateOptions) waitForBackup(ctx context.Context, cmd *cobra.Command, dyn dynamic.Interface) error {
	ctx, cancel := context.WithTimeout(ctx, o.waitTimeout)
	defer cancel()

	ticker := time.NewTicker(o.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out after %s waiting for Backup %s/%s to finish", o.waitTimeout, o.namespace, o.backupName)
		case <-ticker.C:
			obj, err := dyn.Resource(backupGVR()).Namespace(o.namespace).Get(ctx, o.backupName, metav1.GetOptions{})
			if err != nil {
				if apierrors.IsNotFound(err) {
					// The operator garbage-collects expired backups; treat a
					// disappearing resource as a hard failure rather than
					// spinning until the timeout.
					return fmt.Errorf("Backup %s/%s no longer exists", o.namespace, o.backupName)
				}
				return fmt.Errorf("failed to get Backup %s/%s: %w", o.namespace, o.backupName, err)
			}

			backup, err := toBackup(obj)
			if err != nil {
				return err
			}
			if !backup.Status.IsDone() {
				continue
			}

			switch backup.Status.Phase {
			case cnpgv1.BackupPhaseCompleted:
				fmt.Fprintf(cmd.OutOrStdout(), "Backup %s/%s completed.\n", o.namespace, o.backupName)
				return nil
			case preview.BackupPhaseSkipped:
				return fmt.Errorf("Backup %s/%s was skipped: %s", o.namespace, o.backupName, safeValue(backup.Status.Message))
			default:
				return fmt.Errorf("Backup %s/%s failed: %s", o.namespace, o.backupName, safeValue(backup.Status.Message))
			}
		}
	}
}

// ---------------------------------------------------------------------------
// backup list
// ---------------------------------------------------------------------------

type backupListOptions struct {
	documentDBName  string
	scheduledBackup string
	namespace       string
	kubeContext     string
	status          string
	statusFilter    backupStatusFilter
}

func newBackupListCommand() *cobra.Command {
	opts := &backupListOptions{namespace: defaultDocumentDBNamespace, status: string(backupStatusAll)}

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List completed, running, and failed backups",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := opts.complete(); err != nil {
				return err
			}
			return opts.run(cmd.Context(), cmd)
		},
	}

	cmd.Flags().StringVar(&opts.documentDBName, "documentdb", opts.documentDBName, "Only list backups of this DocumentDB resource")
	cmd.Flags().StringVar(&opts.scheduledBackup, "scheduled-backup", opts.scheduledBackup, "Only list backups created by this ScheduledBackup")
	cmd.Flags().StringVarP(&opts.namespace, "namespace", "n", opts.namespace, "Namespace to list backups from")
	cmd.Flags().StringVar(&opts.kubeContext, "context", opts.kubeContext, "Kubeconfig context to use (defaults to current context)")
	cmd.Flags().StringVar(&opts.status, "status", opts.status, "Filter by phase: all, running, completed, failed, or skipped")

	return cmd
}

func (o *backupListOptions) complete() error {
	o.documentDBName = strings.TrimSpace(o.documentDBName)
	o.scheduledBackup = strings.TrimSpace(o.scheduledBackup)
	o.kubeContext = strings.TrimSpace(o.kubeContext)

	o.namespace = strings.TrimSpace(o.namespace)
	if o.namespace == "" {
		o.namespace = defaultDocumentDBNamespace
	}

	status := strings.ToLower(strings.TrimSpace(o.status))
	if status == "" {
		status = string(backupStatusAll)
	}
	switch backupStatusFilter(status) {
	case backupStatusAll, backupStatusRunning, backupStatusCompleted, backupStatusFailed, backupStatusSkipped:
		o.statusFilter = backupStatusFilter(status)
	default:
		return fmt.Errorf("invalid --status %q: must be one of all, running, completed, failed, skipped", o.status)
	}

	return nil
}

func (o *backupListOptions) run(ctx context.Context, cmd *cobra.Command) error {
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

	listOptions := metav1.ListOptions{}
	if o.scheduledBackup != "" {
		listOptions.LabelSelector = fmt.Sprintf("%s=%s", scheduledBackupLabel, o.scheduledBackup)
	}

	list, err := dyn.Resource(backupGVR()).Namespace(o.namespace).List(ctx, listOptions)
	if err != nil {
		return fmt.Errorf("failed to list backups in namespace %q: %w", o.namespace, err)
	}

	backups := make([]preview.Backup, 0, len(list.Items))
	for idx := range list.Items {
		backup, err := toBackup(&list.Items[idx])
		if err != nil {
			return err
		}
		if o.documentDBName != "" && backup.Spec.Cluster.Name != o.documentDBName {
			continue
		}
		if !matchesBackupStatus(backup, o.statusFilter) {
			continue
		}
		backups = append(backups, *backup)
	}

	// Newest first so the most relevant backups are at the top of the table.
	sort.SliceStable(backups, func(i, j int) bool {
		return backups[i].CreationTimestamp.Time.After(backups[j].CreationTimestamp.Time)
	})

	fmt.Fprintf(cmd.OutOrStdout(), "Backups in namespace %s (context %s)\n\n", o.namespace, contextName)

	if len(backups) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "No backups found.")
		return nil
	}

	tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tDOCUMENTDB\tPHASE\tSCHEDULE\tSTARTED\tCOMPLETED\tEXPIRES\tSCHEMA\tMESSAGE")
	for idx := range backups {
		backup := &backups[idx]
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			backup.Name,
			safeValue(backup.Spec.Cluster.Name),
			safeValue(string(backup.Status.Phase)),
			safeValue(backup.Labels[scheduledBackupLabel]),
			formatTime(backup.Status.StartedAt),
			formatTime(backup.Status.StoppedAt),
			formatTime(backup.Status.ExpiredAt),
			safeValue(backup.Status.SchemaVersion),
			safeValue(truncateString(backup.Status.Message, 60)),
		)
	}
	return tw.Flush()
}

func matchesBackupStatus(backup *preview.Backup, filter backupStatusFilter) bool {
	switch filter {
	case backupStatusAll:
		return true
	case backupStatusRunning:
		return !backup.Status.IsDone()
	case backupStatusCompleted:
		return backup.Status.Phase == cnpgv1.BackupPhaseCompleted
	case backupStatusSkipped:
		return backup.Status.Phase == preview.BackupPhaseSkipped
	case backupStatusFailed:
		return backup.Status.IsDone() &&
			backup.Status.Phase != cnpgv1.BackupPhaseCompleted &&
			backup.Status.Phase != preview.BackupPhaseSkipped
	default:
		return true
	}
}

// ---------------------------------------------------------------------------
// backup schedule
// ---------------------------------------------------------------------------

func newBackupScheduleCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "schedule",
		Short: "Manage recurring DocumentDB backup schedules",
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}

	cmd.AddCommand(newBackupScheduleCreateCommand())
	cmd.AddCommand(newBackupScheduleListCommand())

	return cmd
}

type backupScheduleCreateOptions struct {
	documentDBName string
	scheduleName   string
	schedule       string
	namespace      string
	kubeContext    string
	retentionDays  int
}

func newBackupScheduleCreateCommand() *cobra.Command {
	opts := &backupScheduleCreateOptions{namespace: defaultDocumentDBNamespace}

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a recurring backup schedule for a DocumentDB cluster",
		Example: `  # Back up every day at 02:00
  kubectl documentdb backup schedule create --documentdb sample --schedule "0 2 * * *"`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := opts.complete(); err != nil {
				return err
			}
			return opts.run(cmd.Context(), cmd)
		},
	}

	cmd.Flags().StringVar(&opts.documentDBName, "documentdb", opts.documentDBName, "Name of the DocumentDB resource to back up")
	cmd.Flags().StringVar(&opts.scheduleName, "name", opts.scheduleName, "Name of the ScheduledBackup resource (defaults to <documentdb>-schedule)")
	cmd.Flags().StringVar(&opts.schedule, "schedule", opts.schedule, "Cron expression describing when backups run (required)")
	cmd.Flags().StringVarP(&opts.namespace, "namespace", "n", opts.namespace, "Namespace containing the DocumentDB resource")
	cmd.Flags().StringVar(&opts.kubeContext, "context", opts.kubeContext, "Kubeconfig context to use (defaults to current context)")
	cmd.Flags().IntVar(&opts.retentionDays, "retention-days", 0, "Days to retain each backup (defaults to the cluster's backup retention policy)")

	_ = cmd.MarkFlagRequired("documentdb")
	_ = cmd.MarkFlagRequired("schedule")

	return cmd
}

func (o *backupScheduleCreateOptions) complete() error {
	o.documentDBName = strings.TrimSpace(o.documentDBName)
	if o.documentDBName == "" {
		return errors.New("--documentdb is required")
	}

	o.schedule = strings.TrimSpace(o.schedule)
	if o.schedule == "" {
		return errors.New("--schedule is required")
	}
	// Validate with the same parser the operator uses so an invalid expression
	// is rejected here instead of silently stalling reconciliation.
	if _, err := cron.ParseStandard(o.schedule); err != nil {
		return fmt.Errorf("invalid --schedule %q: %w", o.schedule, err)
	}

	o.namespace = strings.TrimSpace(o.namespace)
	if o.namespace == "" {
		o.namespace = defaultDocumentDBNamespace
	}

	o.kubeContext = strings.TrimSpace(o.kubeContext)

	o.scheduleName = strings.TrimSpace(o.scheduleName)
	if o.scheduleName == "" {
		o.scheduleName = o.documentDBName + "-schedule"
	}

	if o.retentionDays < 0 {
		return fmt.Errorf("--retention-days must be greater than zero, got %d", o.retentionDays)
	}

	return nil
}

func (o *backupScheduleCreateOptions) run(ctx context.Context, cmd *cobra.Command) error {
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

	if _, err := dyn.Resource(documentDBGVR()).Namespace(o.namespace).Get(ctx, o.documentDBName, metav1.GetOptions{}); err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("DocumentDB %q not found in namespace %q", o.documentDBName, o.namespace)
		}
		return fmt.Errorf("failed to get DocumentDB %q in namespace %q: %w", o.documentDBName, o.namespace, err)
	}

	scheduledBackup := &preview.ScheduledBackup{
		TypeMeta: metav1.TypeMeta{
			APIVersion: documentDBGVRGroup + "/" + documentDBGVRVersion,
			Kind:       scheduledBackupKind,
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      o.scheduleName,
			Namespace: o.namespace,
		},
		Spec: preview.ScheduledBackupSpec{
			Cluster:  cnpgv1.LocalObjectReference{Name: o.documentDBName},
			Schedule: o.schedule,
		},
	}
	if o.retentionDays > 0 {
		retention := o.retentionDays
		scheduledBackup.Spec.RetentionDays = &retention
	}

	obj, err := toUnstructured(scheduledBackup)
	if err != nil {
		return err
	}

	if _, err := dyn.Resource(scheduledBackupGVR()).Namespace(o.namespace).Create(ctx, obj, metav1.CreateOptions{}); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("ScheduledBackup %q already exists in namespace %q; pass --name to choose a different name", o.scheduleName, o.namespace)
		}
		return fmt.Errorf("failed to create ScheduledBackup %q in namespace %q: %w", o.scheduleName, o.namespace, err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "ScheduledBackup %s/%s created for DocumentDB %q with schedule %q (context %s).\n",
		o.namespace, o.scheduleName, o.documentDBName, o.schedule, contextName)
	return nil
}

type backupScheduleListOptions struct {
	documentDBName string
	namespace      string
	kubeContext    string
}

func newBackupScheduleListCommand() *cobra.Command {
	opts := &backupScheduleListOptions{namespace: defaultDocumentDBNamespace}

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List backup schedules",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := opts.complete(); err != nil {
				return err
			}
			return opts.run(cmd.Context(), cmd)
		},
	}

	cmd.Flags().StringVar(&opts.documentDBName, "documentdb", opts.documentDBName, "Only list schedules targeting this DocumentDB resource")
	cmd.Flags().StringVarP(&opts.namespace, "namespace", "n", opts.namespace, "Namespace to list schedules from")
	cmd.Flags().StringVar(&opts.kubeContext, "context", opts.kubeContext, "Kubeconfig context to use (defaults to current context)")

	return cmd
}

func (o *backupScheduleListOptions) complete() error {
	o.documentDBName = strings.TrimSpace(o.documentDBName)
	o.kubeContext = strings.TrimSpace(o.kubeContext)

	o.namespace = strings.TrimSpace(o.namespace)
	if o.namespace == "" {
		o.namespace = defaultDocumentDBNamespace
	}

	return nil
}

func (o *backupScheduleListOptions) run(ctx context.Context, cmd *cobra.Command) error {
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

	list, err := dyn.Resource(scheduledBackupGVR()).Namespace(o.namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to list scheduled backups in namespace %q: %w", o.namespace, err)
	}

	schedules := make([]preview.ScheduledBackup, 0, len(list.Items))
	for idx := range list.Items {
		var scheduledBackup preview.ScheduledBackup
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(list.Items[idx].Object, &scheduledBackup); err != nil {
			return fmt.Errorf("failed to convert ScheduledBackup %q: %w", list.Items[idx].GetName(), err)
		}
		if o.documentDBName != "" && scheduledBackup.Spec.Cluster.Name != o.documentDBName {
			continue
		}
		schedules = append(schedules, scheduledBackup)
	}

	sort.SliceStable(schedules, func(i, j int) bool {
		return schedules[i].Name < schedules[j].Name
	})

	fmt.Fprintf(cmd.OutOrStdout(), "Backup schedules in namespace %s (context %s)\n\n", o.namespace, contextName)

	if len(schedules) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "No backup schedules found.")
		return nil
	}

	tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tDOCUMENTDB\tSCHEDULE\tRETENTION DAYS\tLAST SCHEDULED\tNEXT SCHEDULED")
	for idx := range schedules {
		scheduledBackup := &schedules[idx]
		retention := "-"
		if scheduledBackup.Spec.RetentionDays != nil {
			retention = fmt.Sprintf("%d", *scheduledBackup.Spec.RetentionDays)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
			scheduledBackup.Name,
			safeValue(scheduledBackup.Spec.Cluster.Name),
			safeValue(scheduledBackup.Spec.Schedule),
			retention,
			formatTime(scheduledBackup.Status.LastScheduledTime),
			formatTime(scheduledBackup.Status.NextScheduledTime),
		)
	}
	return tw.Flush()
}

// ---------------------------------------------------------------------------
// shared helpers
// ---------------------------------------------------------------------------

func toBackup(obj *unstructured.Unstructured) (*preview.Backup, error) {
	var backup preview.Backup
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, &backup); err != nil {
		return nil, fmt.Errorf("failed to convert Backup %q: %w", obj.GetName(), err)
	}
	return &backup, nil
}

func toUnstructured(obj runtime.Object) (*unstructured.Unstructured, error) {
	content, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	if err != nil {
		return nil, fmt.Errorf("failed to convert %T to unstructured: %w", obj, err)
	}
	// The status subresource is server-owned; sending an empty one on create is
	// noise at best and rejected at worst.
	delete(content, "status")
	return &unstructured.Unstructured{Object: content}, nil
}

func formatTime(t *metav1.Time) string {
	if t == nil || t.IsZero() {
		return "-"
	}
	return t.UTC().Format(time.RFC3339)
}
