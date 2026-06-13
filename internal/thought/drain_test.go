// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"context"
	"testing"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// fakeDrainScanner returns scripted segment_rebuild pages in order and records
// the after_id cursor requested on each call so the test can assert the cursor
// advanced to each page's last node_id and terminated on the empty page.
type fakeDrainScanner struct {
	pages    [][]*knowledgev1.PipelineScanItem
	calls    int
	cursors  []string
	pageIter int
}

func (f *fakeDrainScanner) PipelineScan(_ context.Context, req *knowledgev1.PipelineScanRequest) (*knowledgev1.PipelineScanResponse, error) {
	f.calls++
	f.cursors = append(f.cursors, req.GetAfterId())
	if f.pageIter >= len(f.pages) {
		return &knowledgev1.PipelineScanResponse{Items: nil}, nil
	}
	page := f.pages[f.pageIter]
	f.pageIter++
	return &knowledgev1.PipelineScanResponse{Items: page}, nil
}

func item(id string, vec []byte) *knowledgev1.PipelineScanItem {
	return &knowledgev1.PipelineScanItem{NodeId: id, BinaryVector: vec}
}

// TestDrainVectorIndex pages a fake scanner across two full pages plus a
// terminating empty page, asserts the after_id cursor advanced to each page's
// last node_id, and returns the union map keyed by nodeID with the right
// vectors.
func TestDrainVectorIndex(t *testing.T) {
	vA, vB, vC, vD := bitVec(0), bitVec(1), bitVec(2), bitVec(3)
	scanner := &fakeDrainScanner{
		pages: [][]*knowledgev1.PipelineScanItem{
			{item("a", vA), item("b", vB)},
			{item("c", vC), item("d", vD)},
		},
	}

	got, err := drainVectorIndex(context.Background(), scanner)
	if err != nil {
		t.Fatalf("drainVectorIndex error: %v", err)
	}

	// Three PipelineScan calls: page 1, page 2, terminating empty page.
	if scanner.calls != 3 {
		t.Fatalf("PipelineScan calls = %d, want 3", scanner.calls)
	}
	// Cursor: "" (first), "b" (last of page 1), "d" (last of page 2).
	wantCursors := []string{"", "b", "d"}
	for i, want := range wantCursors {
		if scanner.cursors[i] != want {
			t.Fatalf("cursor[%d] = %q, want %q", i, scanner.cursors[i], want)
		}
	}

	want := map[string][]byte{"a": vA, "b": vB, "c": vC, "d": vD}
	if len(got) != len(want) {
		t.Fatalf("map size = %d, want %d", len(got), len(want))
	}
	for id, wv := range want {
		gv, ok := got[id]
		if !ok {
			t.Fatalf("missing node %q in drained index", id)
		}
		if !equalBytes(gv, wv) {
			t.Fatalf("vector[%q] = %x, want %x", id, gv, wv)
		}
	}
}

// TestDrainVectorIndex_Cold asserts an empty first page yields an empty
// (non-nil) map with no error, and a nil scanner returns a degraded-mode error.
func TestDrainVectorIndex_Cold(t *testing.T) {
	// Cold graph: empty first page.
	scanner := &fakeDrainScanner{pages: nil}
	got, err := drainVectorIndex(context.Background(), scanner)
	if err != nil {
		t.Fatalf("cold drainVectorIndex error: %v", err)
	}
	if got == nil {
		t.Fatalf("cold drainVectorIndex returned nil map, want non-nil empty map")
	}
	if len(got) != 0 {
		t.Fatalf("cold map size = %d, want 0", len(got))
	}

	// Nil scanner: degraded-mode error.
	if _, err := drainVectorIndex(context.Background(), nil); err == nil {
		t.Fatalf("nil scanner: want a degraded-mode error, got nil")
	}
}

func equalBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
