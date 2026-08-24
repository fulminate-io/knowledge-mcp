// SPDX-License-Identifier: Apache-2.0

package searchengine

import (
	"testing"
)

// TestFlushSealsSubThresholdBuffer is Phase 4 part (1): an
// engine with MinSegmentDocs well above the doc count buffers everything in the
// active coalescing slice (Add seals nothing), so Search returns nothing and
// Export returns no segments; after Flush the sub-threshold buffer is sealed, so
// Export returns exactly one segment and Search returns the buffered docs.
func TestFlushSealsSubThresholdBuffer(t *testing.T) {
	e := newTestEngine(t, 1024) // MinSegmentDocs=1024, far above the 3 docs below
	defer e.Close()

	if err := e.Add([]Document{doc("d1", "alpha"), doc("d2", "alpha beta"), doc("d3", "gamma")}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	// Pre-Flush: sub-threshold → nothing sealed, nothing searchable/exportable.
	if hits := e.Search(mockQuery{term: "alpha"}, 10); len(hits) != 0 {
		t.Fatalf("pre-Flush Search = %d hits, want 0 (sub-threshold buffer unsealed)", len(hits))
	}
	if blobs := e.Export(); len(blobs) != 0 {
		t.Fatalf("pre-Flush Export = %d segments, want 0", len(blobs))
	}

	// Flush force-seals the buffer.
	if err := e.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	// Post-Flush: exactly one sealed segment, and the buffered docs are searchable.
	if blobs := e.Export(); len(blobs) != 1 {
		t.Fatalf("post-Flush Export = %d segments, want 1", len(blobs))
	}
	hits := e.Search(mockQuery{term: "alpha"}, 10)
	if got := searchIDs(hits); len(got) != 2 || got[0] != "d1" || got[1] != "d2" {
		t.Fatalf("post-Flush Search(alpha) = %v, want [d1 d2]", got)
	}
}

// TestFlushEmptyBufferIsNoOp asserts Flush over an empty buffer seals nothing.
func TestFlushEmptyBufferIsNoOp(t *testing.T) {
	e := newTestEngine(t, 1024)
	defer e.Close()

	if err := e.Flush(); err != nil {
		t.Fatalf("Flush empty: %v", err)
	}
	if blobs := e.Export(); len(blobs) != 0 {
		t.Fatalf("Flush over empty buffer sealed %d segments, want 0", len(blobs))
	}
}

// TestFlushAfterThresholdSealOnlySealsTail asserts Flush seals only the trailing
// sub-threshold tail, not the already-sealed segments (no double-seal).
func TestFlushAfterThresholdSealOnlySealsTail(t *testing.T) {
	e := newTestEngine(t, 2) // seals every 2 docs
	defer e.Close()

	// Two docs → one sealed segment; a third stays buffered.
	if err := e.Add([]Document{doc("a", "x"), doc("b", "x")}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := e.Add([]Document{doc("c", "x")}); err != nil {
		t.Fatalf("Add tail: %v", err)
	}
	if blobs := e.Export(); len(blobs) != 1 {
		t.Fatalf("pre-Flush Export = %d, want 1 (tail unsealed)", len(blobs))
	}
	if err := e.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if blobs := e.Export(); len(blobs) != 2 {
		t.Fatalf("post-Flush Export = %d, want 2 (tail now sealed)", len(blobs))
	}
	if got := searchIDs(e.Search(mockQuery{term: "x"}, 10)); len(got) != 3 {
		t.Fatalf("post-Flush Search(x) = %v, want 3 docs", got)
	}
}
