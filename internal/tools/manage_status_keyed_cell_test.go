// SPDX-License-Identifier: Apache-2.0

package tools

// manage_status_keyed_cell_test.go — requirement 5's keyed-client cell.
//
// The keyless fix taught the segment-coverage cell to read the TEXT pool for a
// graph with no vector engine. A KEYED graph — one that HAS a vector corpus — must
// be unaffected: same readers, same numbers, byte-identical output. This is the
// row that says so, and it is written so the text seam CANNOT be silently
// consulted: the fixture's BM25 reader answers with numbers no vector reader could
// produce, so a cell that reached for it would be caught by its own value.

import (
	"context"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// keyedCellSegReader is a coverageSegReader that ALSO answers the optional text
// seam, with deliberately distinctive numbers.
type keyedCellSegReader struct {
	*coverageSegReader
	bm25Shipped int
	bm25Live    int
	bm25Calls   int
}

func (r *keyedCellSegReader) BM25SegmentDocCounts(
	_ context.Context, _ kgtypes.GraphType, _ string,
) (shipped, live int, skipped bool, err error) {
	r.bm25Calls++
	return r.bm25Shipped, r.bm25Live, false, nil
}

// TestKeyedGraphCoverageCellIgnoresTheTextPool pins that a graph with vectors
// takes the vector readers and ONLY the vector readers.
//
// THE FIXTURE IS ADVERSARIAL BY CONSTRUCTION. The text seam is wired and answers
// 999/888 — values the vector readers never return here — so "the cell rendered
// the vector numbers" and "the cell did not consult the text pool" are two
// independent observations rather than one restated. The call counter is the
// second: a reader that consulted the seam and then discarded its answer would
// still have loaded the BM25 pool, which is work a keyed status read never did.
func TestKeyedGraphCoverageCellIgnoresTheTextPool(t *testing.T) {
	const key = "knowledge/default"
	seg := &keyedCellSegReader{
		coverageSegReader: &coverageSegReader{
			coveredByKey:  map[string]int{key: 150},
			residentByKey: map[string]int{key: 200},
			liveByKey:     map[string]int{key: 100},
		},
		bm25Shipped: 999,
		bm25Live:    888,
	}
	deps := &coverageDeps{gc: &coverageFake{}, segCov: seg}

	// embedded = 42: a keyed graph, which is the whole subject of the row.
	covered, live, hasSeg := segCoveredFor(context.Background(), deps, kgtypes.GraphKnowledge, "default", 42)
	if !hasSeg {
		t.Fatal("a keyed knowledge graph must still report a segment cell")
	}
	if covered != 150 || live != 100 {
		t.Errorf("segCoveredFor(keyed) = (shipped %d, live %d), want (150, 100) — the vector readers' "+
			"own numbers, unchanged by the keyless fix", covered, live)
	}
	if seg.bm25Calls != 0 {
		t.Errorf("the keyed cell consulted the text pool %d time(s); it must not consult it at all — "+
			"reading it would load the BM25 pool on a status read that never did so before", seg.bm25Calls)
	}

	// THE SAME-RUN KNOWN POSITIVE for the counter and the seam: a graph with NO
	// vectors DOES take the text pool, through the identical fixture. Without this
	// the zero above is indistinguishable from a seam that was never wired.
	covered, live, hasSeg = segCoveredFor(context.Background(), deps, kgtypes.GraphKnowledge, "default", 0)
	if !hasSeg || covered != 999 || live != 888 {
		t.Fatalf("KNOWN POSITIVE FAILED: segCoveredFor(keyless) = (%d, %d, hasSeg=%t), want (999, 888, true) "+
			"— the text seam must be reachable, or the keyed assertion above proves nothing",
			covered, live, hasSeg)
	}
	if seg.bm25Calls != 1 {
		t.Errorf("the keyless cell consulted the text pool %d time(s), want exactly 1", seg.bm25Calls)
	}
}

// TestVectorlessCellFallsBackWhenTheTextSeamCannotAnswer pins the third arm: a
// deps that carries no text capability, or whose pool is evicted or fails to load,
// must render exactly what it rendered before this change rather than a fabricated
// pair of zeros presented as a measurement.
func TestVectorlessCellFallsBackWhenTheTextSeamCannotAnswer(t *testing.T) {
	const key = "knowledge/default"
	// No BM25SegmentDocCounts method at all — the twenty-five existing fakes' shape.
	deps := &coverageDeps{gc: &coverageFake{}, segCov: &coverageSegReader{
		coveredByKey:  map[string]int{key: 12},
		residentByKey: map[string]int{key: 12},
		liveByKey:     map[string]int{key: 9},
	}}

	covered, live, hasSeg := segCoveredFor(context.Background(), deps, kgtypes.GraphKnowledge, "default", 0)
	if !hasSeg || covered != 12 || live != 9 {
		t.Errorf("segCoveredFor over a deps with no text capability = (%d, %d, hasSeg=%t), want the vector "+
			"readers' own (12, 9, true) — an absent capability must leave the row exactly as it was, never "+
			"report zeros nobody measured", covered, live, hasSeg)
	}
}
