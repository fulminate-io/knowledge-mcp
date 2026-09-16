// SPDX-License-Identifier: Apache-2.0

// manage_status_coverage_resident_test.go — the PER-FORMAT RESIDENT SEGMENT COUNT
// on `manage(status)`: the resident-growth work's status observable.
//
// The count is the eleventh pinned wire key and the only observable OUTSIDE the
// process that says whether the resident-growth bound is holding. Both surfaces
// carry it: the format:json coverage[] rows, and the markdown table's segment cell,
// which the release smoke reads through a HEADER-LOCATED cell reader — so the value
// has to be readable from the cell taken whole rather than by counting fields, and
// each format has to be NAMED beside its count so a render that silently dropped
// one engine is a failed cell rather than a shorter line.

package tools

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// THE STUB'S METHOD FOR THIS CAPABILITY LIVES HERE, not beside the rest of
// coverageSegReader: that file is against the repository's 500-line cap, and this is
// the only part of the stub whose subject is this file's.

// ResidentSegmentReadings is the optional residentSegmentCountsReader capability:
// the per-format count, its seal-path high-water, and how many {storage,account}
// destinations each was folded from — all three from ONE call, as the production
// seam answers them and as the row that renders them side by side requires.
func (r *coverageSegReader) ResidentSegmentReadings(
	gt kgtypes.GraphType, name string,
) (counts, peaks, destinations map[string]int) {
	key := r.segKey(gt, name)
	return r.residentSegmentsByKey[key], r.residentSegmentPeaksByKey[key], r.residentSegmentDestinationsByKey[key]
}

// residentSegmentsFixture is a probed base row with both engines reporting, plus a
// row whose seam reports nothing at all.
func residentSegmentsFixture() *coverageSegReader {
	return &coverageSegReader{
		coveredByKey:  map[string]int{"code/agent": 40000, "code/plain": 7},
		residentByKey: map[string]int{"code/agent": 40000},
		liveByKey:     map[string]int{"code/agent": 40000, "code/plain": 7},
		residentSegmentsByKey: map[string]map[string]int{
			"code/agent": {"bm25v2": 3412, "hnswv3": 128},
			// code/plain is the BM25-ONLY shape: one format present, and NOT a
			// fabricated hnswv3:0 beside it. An absent engine and an empty one are
			// different facts.
			"code/plain": {"bm25v2": 12},
			// code/knowledge is programmed with NO entry at all — the seam measured
			// nothing for it, which must render as no count rather than as zeros.
		},
		// THE PEAKS DIFFER FROM THE COUNTS ON PURPOSE. They are two readings of one
		// engine — the count after the bound acted, and the high-water the batch's
		// own seals reached before it — so a fixture that made them equal could not
		// tell a surface rendering one from a surface rendering the other.
		residentSegmentPeaksByKey: map[string]map[string]int{
			"code/agent": {"bm25v2": 4151, "hnswv3": 200},
			"code/plain": {"bm25v2": 12},
		},
	}
}

// TestCoverageRows_ResidentSegmentsPerFormat is the json arm.
func TestCoverageRows_ResidentSegmentsPerFormat(t *testing.T) {
	seg := residentSegmentsFixture()
	rows, err := collectCoverageRows(context.Background(), &coverageDeps{gc: branchFixture(), segCov: seg})
	require.NoError(t, err)
	byGraph := make(map[string]CoverageRow, len(rows))
	for _, r := range rows {
		byGraph[r.Graph] = r
	}

	both, ok := byGraph["code/agent"]
	require.True(t, ok)
	assert.Equal(t, map[string]int{"bm25v2": 3412, "hnswv3": 128}, both.ResidentSegments,
		"a graph with both engines reports both, keyed by format: the budget is per format per graph")
	assert.Equal(t, map[string]int{"bm25v2": 4151, "hnswv3": 200}, both.ResidentSegmentsPeak,
		"and the seal-path high-water beside it, which the current count can never show")

	onlyText, ok := byGraph["code/plain"]
	require.True(t, ok)
	assert.Equal(t, map[string]int{"bm25v2": 12}, onlyText.ResidentSegments,
		"a pool holding one format carries THAT format alone; a fabricated hnswv3:0 would state a measurement nobody took")

	unmeasured, ok := byGraph["code/knowledge"]
	require.True(t, ok)
	assert.Nil(t, unmeasured.ResidentSegments,
		"a seam that measured nothing for this graph reports nothing, not a map of zeros")
	assert.Nil(t, unmeasured.ResidentSegmentsPeak, "and the same for its high-water")

	// A BRANCH ROW'S PROBE IS DECLINED AT THE CALLER, so this must report nothing
	// through the same field — and must not have asked the seam for the branch key,
	// which the production reader would lazily construct a manager for.
	branch, ok := byGraph["code/agent@launch-fixes"]
	require.True(t, ok)
	assert.Nil(t, branch.ResidentSegments,
		"a branch graph has no segment pool of its own, so the row must not claim a count for one")
}

// TestCoverageRows_ResidentSegmentsWithNoSeam is the ABSENT-CAPABILITY control: a
// deps whose segment seam does not implement the optional reader reports nothing
// rather than failing or fabricating.
func TestCoverageRows_ResidentSegmentsWithNoSeam(t *testing.T) {
	rows, err := collectCoverageRows(context.Background(), &coverageDeps{gc: branchFixture()})
	require.NoError(t, err)
	for _, r := range rows {
		assert.Nil(t, r.ResidentSegments,
			"row %q: a reader with no way to look has not measured an empty engine, it has not measured", r.Graph)
		assert.Nil(t, r.ResidentSegmentsPeak, "row %q: and the same for its high-water", r.Graph)
	}
}

// TestRenderLLMCoverage_ResidentSegmentsCell is the markdown arm — the surface the
// release smoke reads.
func TestRenderLLMCoverage_ResidentSegmentsCell(t *testing.T) {
	out := renderLLMCoverage(context.Background(),
		&coverageDeps{gc: branchFixture(), segCov: residentSegmentsFixture()})

	assert.Contains(t, out,
		"| code/agent | 51967 | 51967 of 51967 | 51967 of 51967 | "+
			"shipped 40000 · live 40000 [cache-aged] · segments bm25v2 3412 (peak 4151), hnswv3 128 (peak 200) | 0 | 0 |",
		"the segment cell names each format beside its count AND the high-water that count cannot show, "+
			"ordered by format name so the cell is stable")

	assert.Contains(t, out, "· segments bm25v2 12 |",
		"the BM25-only row names its one format and no other, and omits a peak EQUAL to the count — a graph "+
			"that never crossed reads exactly as it did before the peak term existed")

	// KNOWN POSITIVE FOR THE ABSENCE: a row the seam measured nothing for renders the
	// cell WITHOUT the term, in the same render that carries it twice above — so the
	// missing term is this row's honest silence rather than a renderer that lost it.
	assert.NotContains(t, out, "| code/knowledge | 100 | 100 of 100 | 100 of 100 | shipped 0 · live 0 [converged] · segments")
}

// driftingSegReader answers a DIFFERENT triple on every call for the same graph,
// and counts the calls. It is how the one-observation invariant is asserted without
// a race: a row assembled from more than one call takes its count, its peak and its
// destination count from different observations, which is exactly what a child
// binding or being evicted between two walks would produce on a live daemon.
type driftingSegReader struct {
	*coverageSegReader
	mu    sync.Mutex
	calls map[string]int
}

func (r *driftingSegReader) ResidentSegmentReadings(
	gt kgtypes.GraphType, name string,
) (counts, peaks, destinations map[string]int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls[r.segKey(gt, name)]++
	n := r.calls[r.segKey(gt, name)]
	// THE THREE MOVE TOGETHER AND AT DIFFERENT RATES, so a row that read them in
	// three calls cannot look like a row that read them in one.
	return map[string]int{"bm25v2": n}, map[string]int{"bm25v2": n * 10}, map[string]int{"bm25v2": n}
}

func (r *driftingSegReader) observations(gt kgtypes.GraphType, name string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls[r.segKey(gt, name)]
}

// TestCoverageRows_ResidentSegmentReadingsAreOneObservation pins the consistency the
// reader's own documentation promises: the three readings a row renders side by side
// come from ONE walk of the destinations.
//
// THE DRIFTING FIXTURE IS THE RACE, MADE DETERMINISTIC. On a live daemon the
// disagreement is a child binding or being evicted between walks; here it is a
// reader that answers a different triple each time it is asked. Assembled from three
// calls the row reads count 1, peak 20 and destinations 3 — three observations of
// one cell — and every assertion below names which observation it wants.
func TestCoverageRows_ResidentSegmentReadingsAreOneObservation(t *testing.T) {
	seg := &driftingSegReader{
		coverageSegReader: &coverageSegReader{
			coveredByKey: map[string]int{"code/agent": 1},
			liveByKey:    map[string]int{"code/agent": 1},
		},
		calls: map[string]int{},
	}
	rows, err := collectCoverageRows(context.Background(), &coverageDeps{gc: branchFixture(), segCov: seg})
	require.NoError(t, err)

	byGraph := make(map[string]CoverageRow, len(rows))
	for _, r := range rows {
		byGraph[r.Graph] = r
	}
	row, ok := byGraph["code/agent"]
	require.True(t, ok)

	require.Equal(t, 1, seg.observations(kgtypes.GraphCode, "agent"),
		"the row must take its three resident-segment readings in ONE call: a second walk is a second observation, "+
			"and the destinations this client holds engines for can change between them")
	assert.Equal(t, map[string]int{"bm25v2": 1}, row.ResidentSegments)
	assert.Equal(t, map[string]int{"bm25v2": 10}, row.ResidentSegmentsPeak,
		"the peak must come from the SAME observation as the count it qualifies, not from a later walk")
	assert.Equal(t, map[string]int{"bm25v2": 1}, row.ResidentSegmentDestinations,
		"and so must the destination count, or the cell says how many engines a number came from about a number "+
			"read off a different set of them")
}

// TestRenderLLMCoverage_ResidentSegmentsNamesTheDestinationCount is the
// multi-destination arm of that cell.
//
// EACH COUNT IS A MAXIMUM ACROSS THE DESTINATIONS A DAEMON HOLDS ENGINES FOR, so a
// cell that printed only the number would state a maximum over two engines exactly
// as it states a reading of one — and an operator sizing a search fan-out against
// it would be sizing against a pool no search of theirs reaches. The term is named
// ONLY above one, so the single-destination cell every single-account daemon
// renders is byte-identical to what it was before this existed.
func TestRenderLLMCoverage_ResidentSegmentsNamesTheDestinationCount(t *testing.T) {
	seg := residentSegmentsFixture()
	seg.residentSegmentDestinationsByKey = map[string]map[string]int{
		// The two formats DIFFER on purpose: the term is per format, so a fixture
		// naming two destinations for both could not tell a per-format render from a
		// per-row one.
		"code/agent": {"bm25v2": 2, "hnswv3": 1},
		"code/plain": {"bm25v2": 1},
	}
	out := renderLLMCoverage(context.Background(), &coverageDeps{gc: branchFixture(), segCov: seg})

	assert.Contains(t, out,
		"shipped 40000 · live 40000 [cache-aged] · segments bm25v2 3412 (peak 4151, 2 destinations), hnswv3 128 (peak 200) |",
		"the destination count rides INSIDE the format's own parenthesis beside the peak, because both qualify that "+
			"one format's number; the vector format saw one destination and names none")
	assert.Contains(t, out, "· segments bm25v2 12 |",
		"a row whose format was read on ONE destination is byte-identical to what it rendered before this term existed")
	assert.NotContains(t, out, "1 destination",
		"and no cell ever names a single destination: every single-account daemon has exactly one, so naming it would "+
			"put a term on every row of every install to say nothing")
}
