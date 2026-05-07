// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package report

import (
	"strings"
	"testing"
	"time"

	"github.com/documentdb/documentdb-operator/test/longhaul/journal"
	"github.com/documentdb/documentdb-operator/test/longhaul/monitor"
	"github.com/documentdb/documentdb-operator/test/longhaul/workload"
)

func TestGenerateMarkdown_PassMinimal(t *testing.T) {
	s := Summary{
		Result:   ResultPass,
		Duration: 3 * time.Hour,
	}
	md := GenerateMarkdown(s)
	if !strings.Contains(md, "**Result:** PASS") {
		t.Errorf("missing PASS header in: %s", md)
	}
	if !strings.Contains(md, "**Duration:** 3h0m0s") {
		t.Errorf("missing rounded duration in: %s", md)
	}
	if strings.Contains(md, "Failure Reason") {
		t.Errorf("PASS report should not include failure reason")
	}
}

func TestGenerateMarkdown_FailWithReason(t *testing.T) {
	s := Summary{
		Result:     ResultFail,
		Duration:   90 * time.Second,
		FailReason: "policy exceeded on scale-up",
	}
	md := GenerateMarkdown(s)
	if !strings.Contains(md, "**Result:** FAIL") {
		t.Errorf("missing FAIL header")
	}
	if !strings.Contains(md, "policy exceeded on scale-up") {
		t.Errorf("missing fail reason")
	}
}

func TestGenerateMarkdown_DurationRoundedToSeconds(t *testing.T) {
	s := Summary{
		Result:   ResultPass,
		Duration: 2*time.Second + 678*time.Millisecond,
	}
	md := GenerateMarkdown(s)
	if !strings.Contains(md, "**Duration:** 3s") {
		t.Errorf("expected duration rounded to 3s, got: %s", md)
	}
}

func TestGenerateMarkdown_DataPlaneMetricsAlwaysPresent(t *testing.T) {
	s := Summary{
		Result: ResultPass,
		Metrics: workload.MetricsSnapshot{
			WriteAttempted:    1000,
			WriteAcknowledged: 998,
			WriteFailed:       2,
			VerifyPasses:      10,
			GapsDetected:      0,
			ChecksumErrors:    0,
		},
	}
	md := GenerateMarkdown(s)
	for _, want := range []string{
		"## Data Plane Metrics",
		"| Writes Attempted | 1000 |",
		"| Writes Acknowledged | 998 |",
		"| Writes Failed | 2 |",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("missing %q in:\n%s", want, md)
		}
	}
}

func TestGenerateMarkdown_DisruptionWindowsTableOnlyWhenPresent(t *testing.T) {
	noWindows := GenerateMarkdown(Summary{Result: ResultPass})
	if strings.Contains(noWindows, "Disruption Windows") {
		t.Error("Disruption Windows section should be hidden when no windows exist")
	}

	now := time.Now()
	withWindow := GenerateMarkdown(Summary{
		Result: ResultPass,
		Windows: []journal.DisruptionWindow{{
			OperationName: "scale-up",
			StartTime:     now.Add(-30 * time.Second),
			EndTime:       now,
			WriteFailures: 3,
			Policy:        journal.OutagePolicy{MustRecoverWithin: time.Minute, AllowedWriteFailures: 50},
		}},
	})
	if !strings.Contains(withWindow, "Disruption Windows") || !strings.Contains(withWindow, "scale-up") {
		t.Errorf("expected windows table with operation name, got:\n%s", withWindow)
	}
}

func TestGenerateMarkdown_LeakSectionGatedOnSampleCount(t *testing.T) {
	noLeak := GenerateMarkdown(Summary{Result: ResultPass})
	if strings.Contains(noLeak, "Resource Leak Analysis") {
		t.Error("Leak section should be hidden when SampleCount=0")
	}
	withLeak := GenerateMarkdown(Summary{
		Result:       ResultPass,
		LeakAnalysis: monitor.LeakAnalysis{SampleCount: 100, HasLeak: true, MemorySlopeMB: 250.5},
	})
	if !strings.Contains(withLeak, "Resource Leak Analysis") {
		t.Error("Leak section should be present when SampleCount>0")
	}
	if !strings.Contains(withLeak, "Memory leak suspected") {
		t.Error("expected leak warning when HasLeak=true")
	}
}

func TestGenerateMarkdown_RecentEventsTruncatedTo20(t *testing.T) {
	events := make([]journal.Event, 30)
	for i := range events {
		events[i] = journal.Event{
			Timestamp: time.Now(),
			Level:     journal.LevelInfo,
			Component: "x",
			Message:   "evt",
		}
	}
	md := GenerateMarkdown(Summary{Result: ResultPass, Events: events})
	// 20 events + start/end fence + content; assert we didn't dump all 30.
	if got := strings.Count(md, "INFO x: evt"); got != 20 {
		t.Errorf("expected 20 events in report, got %d", got)
	}
}
