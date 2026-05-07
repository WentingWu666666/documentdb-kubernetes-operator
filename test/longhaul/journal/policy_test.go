// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package journal

import (
	"testing"
	"time"
)

func TestDisruptionWindow_IsActive(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name string
		w    DisruptionWindow
		want bool
	}{
		{"open window", DisruptionWindow{StartTime: now}, true},
		{"closed window", DisruptionWindow{StartTime: now, EndTime: now.Add(time.Second)}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.w.IsActive(); got != tt.want {
				t.Errorf("IsActive() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDisruptionWindow_Duration(t *testing.T) {
	t.Run("closed window returns end-start", func(t *testing.T) {
		start := time.Now()
		end := start.Add(7 * time.Second)
		w := DisruptionWindow{StartTime: start, EndTime: end}
		if got := w.Duration(); got != 7*time.Second {
			t.Errorf("Duration() = %v, want 7s", got)
		}
	})
	t.Run("active window returns at-least-since-start", func(t *testing.T) {
		start := time.Now().Add(-3 * time.Second)
		w := DisruptionWindow{StartTime: start}
		got := w.Duration()
		if got < 3*time.Second {
			t.Errorf("Duration() = %v, want >= 3s", got)
		}
	})
}

func TestDisruptionWindow_ExceededPolicy(t *testing.T) {
	tests := []struct {
		name string
		w    DisruptionWindow
		want bool
	}{
		{
			name: "within all budgets",
			w: DisruptionWindow{
				StartTime:     time.Now().Add(-10 * time.Second),
				EndTime:       time.Now(),
				WriteFailures: 5,
				Policy:        OutagePolicy{MustRecoverWithin: time.Minute, AllowedWriteFailures: 50},
			},
			want: false,
		},
		{
			name: "exceeds MustRecoverWithin",
			w: DisruptionWindow{
				StartTime:     time.Now().Add(-2 * time.Minute),
				EndTime:       time.Now(),
				WriteFailures: 1,
				Policy:        OutagePolicy{MustRecoverWithin: time.Minute, AllowedWriteFailures: 50},
			},
			want: true,
		},
		{
			name: "exceeds AllowedWriteFailures",
			w: DisruptionWindow{
				StartTime:     time.Now().Add(-10 * time.Second),
				EndTime:       time.Now(),
				WriteFailures: 100,
				Policy:        OutagePolicy{MustRecoverWithin: time.Minute, AllowedWriteFailures: 50},
			},
			want: true,
		},
		{
			name: "boundary: WriteFailures equal to budget is allowed",
			w: DisruptionWindow{
				StartTime:     time.Now().Add(-10 * time.Second),
				EndTime:       time.Now(),
				WriteFailures: 50,
				Policy:        OutagePolicy{MustRecoverWithin: time.Minute, AllowedWriteFailures: 50},
			},
			want: false,
		},
		{
			name: "active window also evaluated against MustRecoverWithin",
			w: DisruptionWindow{
				StartTime: time.Now().Add(-2 * time.Minute),
				Policy:    OutagePolicy{MustRecoverWithin: time.Minute, AllowedWriteFailures: 50},
			},
			want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.w.ExceededPolicy(); got != tt.want {
				t.Errorf("ExceededPolicy() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDefaultOutagePolicy(t *testing.T) {
	p := DefaultOutagePolicy()
	if p.MustRecoverWithin == 0 || p.AllowedWriteFailures == 0 || p.AllowedDowntime == 0 {
		t.Errorf("DefaultOutagePolicy() returned zero-valued field: %+v", p)
	}
}
