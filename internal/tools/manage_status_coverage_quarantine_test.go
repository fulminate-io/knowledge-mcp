// SPDX-License-Identifier: Apache-2.0

// manage_status_coverage_quarantine_test.go — the QUARANTINE reading on
// `manage(status)`: both surfaces, and the honest silences.
//
// The count is the thirteenth pinned wire key and its consequence is the
// fourteenth. It is the only observable outside the process that says a graph is
// SHORT: a withdrawn segment's documents are unreachable by any search until an
// operator rebuilds the graph's segments, and before this reading existed the sole
// record of that was one log line at the moment it happened.

package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// QuarantinedSegmentCounts is the optional quarantinedSegmentCountsReader
// capability: per-format, how many segments this graph's engines have withdrawn.
func (r *coverageSegReader) QuarantinedSegmentCounts(gt kgtypes.GraphType, name string) map[string]int {
	return r.quarantinedByKey[r.segKey(gt, name)]
}

// quarantineFixture programs three distinguishable states across the branch
// fixture's graphs, which is what lets every assertion below name which state it is
// about: a graph that has LOST segments, a constructed engine that has lost NOTHING
// (a measured zero), and a graph the seam measured not at all.
func quarantineFixture() *coverageSegReader {
	return &coverageSegReader{
		coveredByKey:  map[string]int{"code/agent": 40000, "code/plain": 7},
		residentByKey: map[string]int{"code/agent": 40000},
		liveByKey:     map[string]int{"code/agent": 40000, "code/plain": 7},
		// TWO DESTINATIONS ON THE LOSING ROW: the quarantine count is a SUM across the
		// stores this client holds for the graph while the resident count beside it is
		// a MAXIMUM, so above one destination the cell has to say so.
		residentSegmentsByKey:            map[string]map[string]int{"code/agent": {"bm25v2": 100}},
		residentSegmentDestinationsByKey: map[string]map[string]int{"code/agent": {"bm25v2": 2}},
		quarantinedByKey: map[string]map[string]int{
			// TWO FORMATS, DIFFERENT STATES, IN ONE ROW: the text engine has lost
			// segments and the vector engine has not. A fixture whose formats agreed
			// could not tell a render that names each format from one that sums them.
			"code/agent": {"bm25v2": 2, "hnswv3": 0},
			// A measured zero: this engine exists and has withdrawn nothing.
			"code/plain": {"bm25v2": 0},
			// code/knowledge is programmed with NO entry at all.
		},
	}
}

// TestCoverageRows_QuarantinedSegmentsPerFormat is the json arm.
func TestCoverageRows_QuarantinedSegmentsPerFormat(t *testing.T) {
	rows, err := collectCoverageRows(context.Background(),
		&coverageDeps{gc: branchFixture(), segCov: quarantineFixture()})
	require.NoError(t, err)
	byGraph := make(map[string]CoverageRow, len(rows))
	for _, r := range rows {
		byGraph[r.Graph] = r
	}

	lost, ok := byGraph["code/agent"]
	require.True(t, ok)
	assert.Equal(t, map[string]int{"bm25v2": 2, "hnswv3": 0}, lost.QuarantinedSegments,
		"the withdrawal count is per format, and an engine that lost nothing reports its measured zero")
	assert.Equal(t,
		"2 segment(s) withdrawn: the documents they held are unreachable until this graph's segments are rebuilt "+
			"(manage rebuild_segments with reset: true — a default rebuild scans only what changed and would restore nothing)",
		lost.QuarantinedImpact,
		"the count ships with the sentence saying what it costs: a count alone reads as a statistic")
	assert.Contains(t, lost.QuarantinedImpact, "reset: true",
		"the cure must name the flag: a default rebuild scans only what changed since the last landed rebuild, and a "+
			"quarantine touches neither the node set nor the watermark, so the bare command restores nothing "+
			"(TestBareRebuildIsANoOpOnAnUnchangedCorpusAndResetIsNot)")

	intact, ok := byGraph["code/plain"]
	require.True(t, ok)
	assert.Equal(t, map[string]int{"bm25v2": 0}, intact.QuarantinedSegments,
		"a constructed engine that has withdrawn nothing reports a measured zero")
	assert.Empty(t, intact.QuarantinedImpact,
		"a graph that has lost nothing states no impact — the sentence is a fact about a loss, not a label")

	unmeasured, ok := byGraph["code/knowledge"]
	require.True(t, ok)
	assert.Nil(t, unmeasured.QuarantinedSegments,
		"a seam that measured nothing for this graph reports nothing, not an intact graph")
	assert.Empty(t, unmeasured.QuarantinedImpact)
}

// TestCoverageRows_QuarantineWithNoSeam is the ABSENT-CAPABILITY control: a deps
// whose segment seam does not implement the optional reader reports nothing rather
// than failing or fabricating an intact corpus.
func TestCoverageRows_QuarantineWithNoSeam(t *testing.T) {
	rows, err := collectCoverageRows(context.Background(), &coverageDeps{gc: branchFixture()})
	require.NoError(t, err)
	for _, r := range rows {
		assert.Nil(t, r.QuarantinedSegments,
			"row %q: a reader with no way to look has not observed an intact graph", r.Graph)
		assert.Empty(t, r.QuarantinedImpact, "row %q", r.Graph)
	}
}

// TestRenderLLMCoverage_QuarantineCell is the markdown arm.
//
// THE KNOWN NEGATIVE IS IN THE SAME RENDER as the positive, which is what makes the
// absence a measurement: code/plain's engine reported a zero and code/knowledge
// reported nothing, and neither carries the term while code/agent does.
func TestRenderLLMCoverage_QuarantineCell(t *testing.T) {
	out := renderLLMCoverage(context.Background(),
		&coverageDeps{gc: branchFixture(), segCov: quarantineFixture()})

	assert.Contains(t, out,
		"· QUARANTINED bm25v2 2 across 2 destinations "+
			"(documents unreachable until this graph's segments are rebuilt — manage rebuild_segments, reset: true)",
		"the losing row names the format, the count, how many stores it was summed from, what it costs, "+
			"and the flag without which the cure is a no-op")
	assert.Contains(t, out, "· segments bm25v2 100 (2 destinations)",
		"control: the neighboring resident term discloses its own aggregation, which is why the quarantine term must")
	assert.NotContains(t, out, "QUARANTINED bm25v2 0 across",
		"a single-destination row renders no qualifier — the term is named only where the numbers can disagree")
	assert.NotContains(t, out, "QUARANTINED hnswv3",
		"a format that has withdrawn nothing is absent from the term rather than present as a zero")
	assert.Equal(t, 1, countOccurrences(out, "QUARANTINED"),
		"exactly one row carries the term: the row that has lost segments, and no other")
}

// countOccurrences counts non-overlapping occurrences of sub in s, so a row can
// assert that exactly ONE row of the rendered table carries the term.
func countOccurrences(s, sub string) int {
	n, at := 0, 0
	for {
		i := strings.Index(s[at:], sub)
		if i < 0 {
			return n
		}
		n++
		at += i + len(sub)
	}
}
