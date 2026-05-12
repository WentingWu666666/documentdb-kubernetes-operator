// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package report

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/documentdb/documentdb-operator/test/longhaul/monitor"
	"github.com/documentdb/documentdb-operator/test/longhaul/workload"
)

// withGitHubActionsEnv toggles the GITHUB_ACTIONS env var for the duration of fn.
func withGitHubActionsEnv(t *testing.T, value string, fn func()) {
	t.Helper()
	prev, had := os.LookupEnv("GITHUB_ACTIONS")
	if err := os.Setenv("GITHUB_ACTIONS", value); err != nil {
		t.Fatalf("setenv: %v", err)
	}
	defer func() {
		if had {
			_ = os.Setenv("GITHUB_ACTIONS", prev)
		} else {
			_ = os.Unsetenv("GITHUB_ACTIONS")
		}
	}()
	fn()
}

// captureStdout redirects os.Stdout for the duration of fn and returns what was written.
// EmitAnnotation uses fmt.Printf which writes directly to os.Stdout, so we need this
// rather than a *bytes.Buffer.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	orig := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = orig }()

	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()
	fn()
	_ = w.Close()
	return <-done
}

func TestEmitAnnotation_NoOpOutsideGitHub(t *testing.T) {
	withGitHubActionsEnv(t, "", func() {
		out := captureStdout(t, func() {
			EmitAnnotation(Summary{Result: ResultFail, FailReason: "boom"})
		})
		if out != "" {
			t.Errorf("expected silent no-op outside Actions, got: %q", out)
		}
	})
}

func TestEmitAnnotation_FailEmitsErrorAnnotation(t *testing.T) {
	withGitHubActionsEnv(t, "true", func() {
		out := captureStdout(t, func() {
			EmitAnnotation(Summary{Result: ResultFail, FailReason: "data loss detected"})
		})
		if !strings.Contains(out, "::error") {
			t.Errorf("missing ::error annotation in: %q", out)
		}
		if !strings.Contains(out, "data loss detected") {
			t.Errorf("missing fail reason in: %q", out)
		}
	})
}

func TestEmitAnnotation_FailWithoutReasonStillEmitsError(t *testing.T) {
	withGitHubActionsEnv(t, "true", func() {
		out := captureStdout(t, func() {
			EmitAnnotation(Summary{Result: ResultFail})
		})
		if !strings.Contains(out, "::error") {
			t.Errorf("expected ::error even without FailReason, got: %q", out)
		}
		if !strings.Contains(out, "Long haul test FAILED") {
			t.Errorf("expected default fail message, got: %q", out)
		}
	})
}

func TestEmitAnnotation_PassEmitsNotice(t *testing.T) {
	withGitHubActionsEnv(t, "true", func() {
		out := captureStdout(t, func() {
			EmitAnnotation(Summary{
				Result:      ResultPass,
				Duration:    2 * time.Hour,
				OpsExecuted: 17,
				Metrics:     workload.MetricsSnapshot{WriteAttempted: 1234, GapsDetected: 0},
			})
		})
		if !strings.Contains(out, "::notice") {
			t.Errorf("missing ::notice annotation in: %q", out)
		}
		if !strings.Contains(out, "1234") || !strings.Contains(out, "17") {
			t.Errorf("annotation missing metrics, got: %q", out)
		}
	})
}

func TestEmitAnnotation_LeakWarningEmittedRegardlessOfResult(t *testing.T) {
	leak := monitor.LeakAnalysis{
		HasLeak:       true,
		MemorySlopeMB: 12.5,
		Duration:      90 * time.Minute,
		SampleCount:   60,
	}
	cases := []struct{ name string; res Result }{
		{"on PASS", ResultPass},
		{"on FAIL", ResultFail},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withGitHubActionsEnv(t, "true", func() {
				out := captureStdout(t, func() {
					EmitAnnotation(Summary{Result: tc.res, LeakAnalysis: leak})
				})
				if !strings.Contains(out, "::warning") {
					t.Errorf("missing leak ::warning in: %q", out)
				}
				if !strings.Contains(out, "12.50") {
					t.Errorf("missing slope value in: %q", out)
				}
			})
		})
	}
}

func TestEmitAnnotation_NoLeakNoWarning(t *testing.T) {
	withGitHubActionsEnv(t, "true", func() {
		out := captureStdout(t, func() {
			EmitAnnotation(Summary{Result: ResultPass, LeakAnalysis: monitor.LeakAnalysis{HasLeak: false}})
		})
		if strings.Contains(out, "::warning") {
			t.Errorf("unexpected leak warning when HasLeak=false, got: %q", out)
		}
	})
}
