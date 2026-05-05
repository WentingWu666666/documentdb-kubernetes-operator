// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package main provides a standalone binary entry point for running
// long haul tests as a Kubernetes Job (without Ginkgo test framework).
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/documentdb/documentdb-operator/test/longhaul/config"
	"github.com/documentdb/documentdb-operator/test/longhaul/journal"
	"github.com/documentdb/documentdb-operator/test/longhaul/monitor"
	"github.com/documentdb/documentdb-operator/test/longhaul/operations"
	"github.com/documentdb/documentdb-operator/test/longhaul/report"
	"github.com/documentdb/documentdb-operator/test/longhaul/workload"

	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

func main() {
	log.SetFlags(log.Ltime | log.Lmsgprefix)
	log.SetPrefix("[longhaul] ")

	cfg, err := config.LoadFromEnv()
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		log.Fatalf("invalid config: %v", err)
	}

	log.Printf("config loaded: duration=%s namespace=%s cluster=%s writers=%d verifiers=%d",
		cfg.MaxDuration, cfg.Namespace, cfg.ClusterName, cfg.NumWriters, cfg.NumVerifiers)

	exitCode := run(cfg)
	os.Exit(exitCode)
}

func run(cfg config.Config) int {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if cfg.MaxDuration > 0 {
		var timeoutCancel context.CancelFunc
		ctx, timeoutCancel = context.WithTimeout(ctx, cfg.MaxDuration)
		defer timeoutCancel()
	}

	// Initialize components.
	j := journal.New()
	metrics := workload.NewMetrics()

	// Connect to MongoDB.
	if cfg.MongoURI == "" {
		log.Fatal("LONGHAUL_MONGO_URI must be set")
	}
	mongoClient, err := mongo.Connect(options.Client().ApplyURI(cfg.MongoURI))
	if err != nil {
		log.Fatalf("failed to connect to MongoDB: %v", err)
	}
	defer func() {
		disconnectCtx, c := context.WithTimeout(context.Background(), 5*time.Second)
		defer c()
		_ = mongoClient.Disconnect(disconnectCtx)
	}()

	// Verify connectivity.
	pingCtx, pingCancel := context.WithTimeout(ctx, 10*time.Second)
	defer pingCancel()
	if err := mongoClient.Ping(pingCtx, nil); err != nil {
		log.Fatalf("MongoDB ping failed: %v", err)
	}
	log.Println("MongoDB connection established")

	db := mongoClient.Database("longhaul")

	// Drop previous test data to avoid duplicate key conflicts.
	if err := db.Collection(workload.CollectionName).Drop(ctx); err != nil {
		log.Fatalf("failed to drop collection: %v", err)
	}

	// Create indexes.
	if err := workload.EnsureIndexes(ctx, db); err != nil {
		log.Fatalf("failed to create indexes: %v", err)
	}

	j.Info("main", "long haul test starting")

	// Initialize cluster client (placeholder — real implementation in Phase 2).
	clusterClient := &placeholderClusterClient{replicas: cfg.MinReplicas}

	// Start health monitor.
	healthMon := monitor.NewHealthMonitor(clusterClient, j, cfg.SteadyStateWait)
	go healthMon.Run(ctx)

	// Start leak detector.
	leakDetector := monitor.NewLeakDetector(j, 10.0, 10)

	// Start writers.
	workload.StartWriters(ctx, cfg.NumWriters, db, metrics, j)
	j.Info("main", fmt.Sprintf("started %d writers", cfg.NumWriters))

	// Start verifiers.
	workload.StartVerifiers(ctx, cfg.NumVerifiers, db, metrics, j)
	j.Info("main", fmt.Sprintf("started %d verifiers", cfg.NumVerifiers))

	// Configure operations.
	ops := []operations.Operation{
		operations.NewScaleUp(clusterClient, healthMon, cfg.MaxReplicas, cfg.RecoveryTimeout),
		operations.NewScaleDown(clusterClient, healthMon, cfg.MinReplicas, cfg.RecoveryTimeout),
	}

	// Start operation scheduler.
	scheduler := operations.NewScheduler(ops, healthMon, j, cfg.OpCooldown)
	go scheduler.Run(ctx)

	j.Info("main", "all components started, entering main loop")

	// Main loop: wait for context expiry.
	<-ctx.Done()
	j.Info("main", fmt.Sprintf("test ending: %v", ctx.Err()))

	// Allow goroutines to flush.
	time.Sleep(500 * time.Millisecond)

	// Generate report.
	snap := metrics.Snapshot()
	leakAnalysis := leakDetector.Analyze()

	result := report.ResultPass
	failReason := ""

	if snap.HasDataLoss() {
		result = report.ResultFail
		failReason = fmt.Sprintf("data loss: %d gaps, %d checksum errors",
			snap.GapsDetected, snap.ChecksumErrors)
	}
	if j.HasPolicyViolation() {
		result = report.ResultFail
		if failReason != "" {
			failReason += "; "
		}
		failReason += "outage policy violated"
	}

	summary := report.Summary{
		Result:       result,
		Duration:     snap.Elapsed,
		Metrics:      snap,
		LeakAnalysis: leakAnalysis,
		OpsExecuted:  scheduler.OpsExecuted(),
		Windows:      j.DisruptionWindows(),
		Events:       j.Events(),
		FailReason:   failReason,
	}

	markdown := report.GenerateMarkdown(summary)
	fmt.Println("\n" + markdown)

	if result == report.ResultFail {
		log.Printf("TEST FAILED: %s", failReason)
		return 1
	}

	log.Println("TEST PASSED")
	return 0
}

// placeholderClusterClient is a minimal implementation of monitor.ClusterClient
// that returns safe defaults. It will be replaced with a real k8s client in Phase 2.
type placeholderClusterClient struct {
	replicas int
}

func (p *placeholderClusterClient) GetClusterHealth(_ context.Context) (monitor.ClusterHealth, error) {
	return monitor.ClusterHealth{
		Timestamp:     time.Now(),
		AllPodsReady:  true,
		ReadyPods:     p.replicas,
		TotalPods:     p.replicas,
		CRReady:       true,
		RestartCount:  0,
		WritesHealthy: true,
	}, nil
}

func (p *placeholderClusterClient) GetCurrentReplicas(_ context.Context) (int, error) {
	return p.replicas, nil
}

func (p *placeholderClusterClient) ScaleCluster(_ context.Context, replicas int) error {
	p.replicas = replicas
	return nil
}
