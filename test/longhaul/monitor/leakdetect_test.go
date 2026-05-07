// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package monitor

import (
	"testing"
	"time"

	"github.com/documentdb/documentdb-operator/test/longhaul/journal"
)

func TestLeakDetector_BelowMinSamplesReturnsEmpty(t *testing.T) {
	d := NewLeakDetector(journal.New(), 10, 5)
	d.AddSample(ResourceSample{Timestamp: time.Now(), MemoryMB: 100})
	a := d.Analyze()
	if a.HasLeak || a.MemorySlopeMB != 0 {
		t.Errorf("Analyze with too few samples should return zeroed slope, got %+v", a)
	}
	if a.SampleCount != 1 {
		t.Errorf("SampleCount = %d, want 1", a.SampleCount)
	}
}

func TestLeakDetector_FlatMemoryNoLeak(t *testing.T) {
	d := NewLeakDetector(journal.New(), 1.0, 3)
	t0 := time.Now()
	for i := 0; i < 10; i++ {
		d.AddSample(ResourceSample{
			Timestamp: t0.Add(time.Duration(i) * time.Second),
			MemoryMB:  100,
			CPUCores:  0.5,
		})
	}
	a := d.Analyze()
	if a.HasLeak {
		t.Errorf("HasLeak = true on flat memory: %+v", a)
	}
	if absf(a.MemorySlopeMB) > 0.001 {
		t.Errorf("MemorySlopeMB = %v on flat samples, want ~0", a.MemorySlopeMB)
	}
}

func TestLeakDetector_GrowingMemoryDetectsLeak(t *testing.T) {
	// Threshold is 100 MB/hour. Grow at 1 MB/sec = 3600 MB/hour: well above.
	d := NewLeakDetector(journal.New(), 100.0, 3)
	t0 := time.Now()
	for i := 0; i < 60; i++ {
		d.AddSample(ResourceSample{
			Timestamp: t0.Add(time.Duration(i) * time.Second),
			MemoryMB:  100 + float64(i),
		})
	}
	a := d.Analyze()
	if !a.HasLeak {
		t.Errorf("HasLeak = false on growing memory: slope=%v", a.MemorySlopeMB)
	}
	// 1 MB/sec * 3600 = 3600 MB/hour ± noise
	if a.MemorySlopeMB < 3500 || a.MemorySlopeMB > 3700 {
		t.Errorf("MemorySlopeMB = %v, want ~3600", a.MemorySlopeMB)
	}
}

func TestLeakDetector_GrowthBelowThresholdNoLeak(t *testing.T) {
	// Grow at 0.01 MB/sec = 36 MB/hour, threshold 100 MB/hour -> not a leak.
	d := NewLeakDetector(journal.New(), 100.0, 3)
	t0 := time.Now()
	for i := 0; i < 60; i++ {
		d.AddSample(ResourceSample{
			Timestamp: t0.Add(time.Duration(i) * time.Second),
			MemoryMB:  100 + 0.01*float64(i),
		})
	}
	a := d.Analyze()
	if a.HasLeak {
		t.Errorf("HasLeak = true on sub-threshold growth: slope=%v", a.MemorySlopeMB)
	}
}

func TestLeakDetector_MinSamplesFloor(t *testing.T) {
	d := NewLeakDetector(journal.New(), 10, 1) // request 1, but constructor floors to 3
	d.AddSample(ResourceSample{Timestamp: time.Now(), MemoryMB: 100})
	d.AddSample(ResourceSample{Timestamp: time.Now(), MemoryMB: 100})
	if a := d.Analyze(); a.MemorySlopeMB != 0 {
		t.Errorf("expected zero slope with 2 samples (floor=3), got %+v", a)
	}
}

func absf(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}
