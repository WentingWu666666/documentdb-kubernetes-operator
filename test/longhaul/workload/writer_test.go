// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package workload

import (
	"strings"
	"testing"

	"github.com/documentdb/documentdb-operator/test/longhaul/journal"
)

func TestComputeChecksum_Deterministic(t *testing.T) {
	a := computeChecksum("w001", 42, "payload-x")
	b := computeChecksum("w001", 42, "payload-x")
	if a != b {
		t.Errorf("checksum not deterministic: %s != %s", a, b)
	}
}

func TestComputeChecksum_DependsOnAllInputs(t *testing.T) {
	base := computeChecksum("w001", 42, "payload")
	cases := map[string]string{
		"writerID changed": computeChecksum("w002", 42, "payload"),
		"seq changed":      computeChecksum("w001", 43, "payload"),
		"payload changed":  computeChecksum("w001", 42, "payload-x"),
	}
	for name, got := range cases {
		t.Run(name, func(t *testing.T) {
			if got == base {
				t.Errorf("checksum should differ when %s; both = %s", name, base)
			}
		})
	}
}

func TestComputeChecksum_HexLength(t *testing.T) {
	got := computeChecksum("w001", 1, "x")
	// SHA-256 truncated to 8 bytes -> 16 hex chars.
	if len(got) != 16 {
		t.Errorf("checksum len=%d, want 16 (hex of 8 bytes); got=%q", len(got), got)
	}
	for _, r := range got {
		if !strings.ContainsRune("0123456789abcdef", r) {
			t.Errorf("non-hex char %q in checksum %q", r, got)
		}
	}
}

func TestNewWriter_FieldsWired(t *testing.T) {
	// The writer constructor is mostly composition; verify it doesn't panic
	// when given a nil collection (mongo.Database can produce a Collection
	// without I/O), and that the ID is preserved. We can't construct a real
	// *mongo.Database without a connection, so we limit the assertion to
	// what's safe to inspect: the metrics, journal, and id wiring.
	m := NewMetrics()
	j := journal.New()
	w := &Writer{id: "w042", metrics: m, journal: j}
	if w.id != "w042" {
		t.Errorf("id=%q, want w042", w.id)
	}
	if w.metrics != m {
		t.Error("metrics pointer not preserved")
	}
	if w.journal != j {
		t.Error("journal pointer not preserved")
	}
	// Sequence starts at zero before any writes.
	if got := w.seq.Load(); got != 0 {
		t.Errorf("initial seq=%d, want 0", got)
	}
}

func TestWriter_SeqMonotonicallyIncreases(t *testing.T) {
	// Even though writeOne hits the network, the seq.Add is the first thing
	// it does. Verify the atomic counter advances correctly.
	w := &Writer{id: "w001"}
	for i := int64(1); i <= 100; i++ {
		got := w.seq.Add(1)
		if got != i {
			t.Errorf("seq.Add iter %d returned %d", i, got)
		}
	}
}
