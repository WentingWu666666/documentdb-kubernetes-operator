// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package operations

import (
	"context"
	"fmt"
	"time"

	"github.com/documentdb/documentdb-operator/test/longhaul/journal"
	"github.com/documentdb/documentdb-operator/test/longhaul/monitor"
)

// KillPrimaryPod deletes the CNPG primary pod to exercise the automatic
// failover path: CNPG must promote a standby, and the cluster must return to
// steady state within the recovery budget. The continuous workload verifier
// independently catches any data loss caused by the failover.
type KillPrimaryPod struct {
	client              monitor.ClusterClient
	healthMon           SteadyStateGate
	recovery            time.Duration
	primaryPollInterval time.Duration
}

// NewKillPrimaryPod creates a KillPrimaryPod operation.
func NewKillPrimaryPod(client monitor.ClusterClient, health SteadyStateGate, recovery time.Duration) *KillPrimaryPod {
	return &KillPrimaryPod{
		client:              client,
		healthMon:           health,
		recovery:            recovery,
		primaryPollInterval: time.Second,
	}
}

func (k *KillPrimaryPod) Name() string { return "kill-primary-pod" }

func (k *KillPrimaryPod) Weight() int { return 2 }

// Precondition requires at least one standby (instancesPerNode>=2). Killing the
// sole instance of a single-instance cluster would cause guaranteed downtime
// with no failover target — a true-but-useless policy violation. The same guard
// (and rationale) is used by UpgradeDocumentDB; skips don't consume the
// scheduler cooldown, so this is free to re-evaluate on the next tick.
func (k *KillPrimaryPod) Precondition(ctx context.Context) (bool, string) {
	ipn, err := k.client.GetInstancesPerNode(ctx)
	if err != nil {
		return false, fmt.Sprintf("cannot read instancesPerNode: %v", err)
	}
	if ipn < 2 {
		return false, fmt.Sprintf("instancesPerNode=%d (no HA standby) — killing primary would cause real downtime; skipping", ipn)
	}
	return true, ""
}

func (k *KillPrimaryPod) Execute(ctx context.Context) error {
	primary, err := k.client.GetPrimaryInstance(ctx)
	if err != nil {
		return fmt.Errorf("get primary instance: %w", err)
	}
	if primary == "" {
		return fmt.Errorf("get primary instance: cluster returned an empty primary pod name")
	}
	if k.healthMon == nil {
		return fmt.Errorf("kill-primary-pod: health monitor is nil")
	}

	recoveryCtx, cancel := context.WithTimeout(ctx, k.recovery)
	defer cancel()

	if err := k.client.DeletePod(recoveryCtx, primary); err != nil {
		return fmt.Errorf("delete primary pod %s: %w", primary, err)
	}

	if err := k.waitForPrimaryChange(recoveryCtx, primary); err != nil {
		return err
	}

	// A changed primary proves CNPG promoted a standby rather than merely
	// recreating the deleted pod and reporting the old primary again.
	if err := k.healthMon.WaitForSteadyState(recoveryCtx); err != nil {
		return fmt.Errorf("wait for steady-state recovery: %w", err)
	}

	current, err := k.client.GetPrimaryInstance(recoveryCtx)
	if err != nil {
		return fmt.Errorf("verify primary after steady-state recovery: %w", err)
	}
	if current == "" || current == primary {
		return fmt.Errorf("verify primary after steady-state recovery: expected a non-empty primary different from %q, got %q",
			primary, current)
	}
	return nil
}

func (k *KillPrimaryPod) waitForPrimaryChange(ctx context.Context, original string) error {
	ticker := time.NewTicker(k.primaryPollInterval)
	defer ticker.Stop()

	lastObserved := original
	var lastErr error
	for {
		current, err := k.client.GetPrimaryInstance(ctx)
		if err == nil {
			lastObserved = current
			if current != "" && current != original {
				return nil
			}
		} else {
			lastErr = err
		}

		select {
		case <-ctx.Done():
			if lastErr != nil {
				return fmt.Errorf("primary did not change from %q before recovery timeout (last read error: %v): %w",
					original, lastErr, ctx.Err())
			}
			return fmt.Errorf("primary did not change from %q before recovery timeout (last observed %q): %w",
				original, lastObserved, ctx.Err())
		case <-ticker.C:
		}
	}
}

// OutagePolicy bounds the write outage of an automatic failover. Killing the
// primary interrupts writes until CNPG detects the loss and promotes a standby,
// so it uses the single-primary-handover budget (journal.PrimaryHandoverPolicy,
// ~30s). upgrade-documentdb has its own, larger budget
// (journal.UpgradeOutagePolicy, ~90s): its graceful switchover coincides with
// the extension migration under live write load.
func (k *KillPrimaryPod) OutagePolicy() journal.OutagePolicy {
	return journal.PrimaryHandoverPolicy(k.recovery)
}
