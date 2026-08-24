// SPDX-License-Identifier: Apache-2.0

// manage_status_coverage_table_shape_test.go — the coverage table's markdown SHAPE.
//
// A markdown renderer silently DROPS every cell past the header's column count, so
// a row wider than its header does not render badly, it renders INCOMPLETELY and
// says nothing about it. That is how the erasure-backlog cell — the server's
// deletion-backlog signal — was emitted by every populated row and displayed by
// none of them.

package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// markdownCells counts one rendered markdown table line's cells: the segments
// between the leading and trailing pipes. It is DERIVED FROM THE RENDERED STRING
// rather than from any literal in the source, so a future column addition that
// updates all four sites keeps this test green and one that updates three of them
// turns it red.
func markdownCells(line string) int {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "|") || !strings.HasSuffix(trimmed, "|") {
		return -1
	}
	return len(strings.Split(strings.Trim(trimmed, "|"), "|"))
}

// TestCoverageTableHeaderMatchesRowCellCount asserts the header, the separator and
// BOTH row shapes — the populated row and the "(empty graph)" row — carry the same
// number of cells.
func TestCoverageTableHeaderMatchesRowCellCount(t *testing.T) {
	fake := &coverageFake{statsByKey: map[string]*knowledgev1.GraphStats{
		// A populated graph (the wide row) and an EMPTY one, because the empty-graph
		// row is rendered by a different format string and is exactly the site a
		// column addition forgets.
		"code/myrepo": {NonProxyNodeCount: 8, SummarizedCount: 8, BinaryVectorCount: 8},
		"practice/go": {NonProxyNodeCount: 0},
	}}
	seg := &coverageSegReader{
		coveredByKey:  map[string]int{"code/myrepo": 6},
		residentByKey: map[string]int{"code/myrepo": 6},
	}

	out := renderLLMCoverage(context.Background(), &coverageDeps{gc: fake, segCov: seg})
	require.NotEmpty(t, out)

	var header, separator int
	rows := map[string]int{}
	for line := range strings.SplitSeq(out, "\n") {
		n := markdownCells(line)
		if n < 0 {
			continue
		}
		switch {
		case strings.Contains(line, "| graph |"):
			header = n
		case strings.HasPrefix(strings.TrimSpace(line), "| --- |"):
			separator = n
		default:
			rows[strings.TrimSpace(line)] = n
		}
	}

	// KNOWN-POSITIVE CONTROLS for the equality below: an equality between two
	// numbers both derived from a table that rendered nothing would be vacuously
	// true, so pin that the header was found, that it is wider than a trivial table,
	// and that BOTH row shapes are present.
	require.Positive(t, header, "the header row must have been rendered and recognized")
	require.GreaterOrEqual(t, header, 8, "the table carries at least the eight columns it emits")
	var sawEmpty, sawPopulated bool
	for line := range rows {
		if strings.Contains(line, "(empty graph)") {
			sawEmpty = true
			continue
		}
		sawPopulated = true
	}
	require.True(t, sawEmpty, "the (empty graph) row shape must be among the rows measured")
	require.True(t, sawPopulated, "the populated row shape must be among the rows measured")

	require.Equal(t, header, separator, "the separator must carry the header's column count")
	for line, n := range rows {
		require.Equal(t, header, n,
			"every row must carry the header's column count — a wider row has its trailing cells silently dropped by the renderer: %s", line)
	}
}
