// SPDX-License-Identifier: Apache-2.0

package recipe

import (
	"context"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// run_recipe_offset_test.go covers the extract cursor end to end through
// RunRecipe, which is where Options.Offset, effectiveOffset and the skip in
// recordExtractRow are actually wired together.
//
// FIXTURE HAZARD, stated because it is not inferable: extractSourceCaller names
// its sections s0..sN and they carry no edges, so under the reading-order index
// they are all unranked and order by NODE ID. Every n here stays below 10 — at
// ten sections "s10" sorts before "s2" and the expected sequences stop being the
// obvious ones.

// sourceIDs projects an extract result down to the source node behind each row.
func sourceIDs(ex *ExtractResult) []string {
	out := make([]string, 0, len(ex.Rows))
	for _, row := range ex.Rows {
		out = append(out, row.SourceNodeID)
	}
	return out
}

// runOffsetExtract runs the standard section-per-row extract over n sections at
// the given cursor and row cap.
func runOffsetExtract(t *testing.T, n, offset, maxRows int) *Result {
	t.Helper()
	opts := extractOpts(extractBody)
	opts.Offset = offset
	opts.MaxRows = maxRows
	res, err := RunRecipe(context.Background(), extractSourceCaller(n), "doc", kgtypes.GraphWebRaw, opts)
	require.NoError(t, err)
	require.NotNil(t, res.Extract, "extract mode must populate Extract")
	return res
}

func TestRunRecipe_ExtractOffsetWindow(t *testing.T) {
	t.Run("window_starts_at_offset", func(t *testing.T) {
		res := runOffsetExtract(t, 8, 3, 2)
		assert.Equal(t, []string{"s3", "s4"}, sourceIDs(res.Extract))
		assert.Equal(t, 8, res.Extract.RowsMatched,
			"the matched total counts the WHOLE population, not the page")
		assert.Equal(t, 2, res.Extract.RowsReturned)
	})

	// THE OFF-BY-ONE TEST. A cursor that is one out in either direction still
	// produces a plausible single window; only tiling the whole population and
	// demanding each row exactly once catches it.
	t.Run("pages_tile_without_gap_or_overlap", func(t *testing.T) {
		const n, page = 7, 3
		var seen []string
		for offset := 0; offset < n; offset += page {
			seen = append(seen, sourceIDs(runOffsetExtract(t, n, offset, page).Extract)...)
		}
		want := make([]string, 0, n)
		for i := range n {
			want = append(want, "s"+strconv.Itoa(i))
		}
		assert.Equal(t, want, seen,
			"three pages of three over seven rows must cover every row exactly once, in order")
	})

	t.Run("offset_past_the_end_returns_nothing_but_still_counts", func(t *testing.T) {
		res := runOffsetExtract(t, 5, 10, 3)
		assert.Empty(t, res.Extract.Rows, "no row sits at or after the cursor")
		assert.Equal(t, 5, res.Extract.RowsMatched,
			"the population behind the cursor is still reported — an overshoot is not an empty match")
	})

	// EMISSION IS NOT BOUNDED BY THE CURSOR. Nodes, Lineage and every Stats
	// counter accumulate exactly as they always have; the Extract row list is
	// the only bounded thing. An implementation that skips emission for
	// pre-cursor rows passes every row assertion above and is rejected here.
	t.Run("emission_is_unchanged_by_the_cursor", func(t *testing.T) {
		res := runOffsetExtract(t, 6, 4, 2)
		assert.Len(t, res.Extract.Rows, 2, "the page is bounded")
		assert.Len(t, res.Nodes, 6, "every matched row still emitted a node")
		assert.Len(t, res.Lineage, 6, "every emitted node still carries its translated-from edge")
		assert.Equal(t, 6, res.Stats.NodesEmitted)
	})
}

// TestRunRecipe_ExtractNegativeOffsetIsRefused is the bad-input-errors leg: a
// clamping implementation returns a successful FIRST page for a request that
// was wrong, and satisfies every row assertion above while doing it.
func TestRunRecipe_ExtractNegativeOffsetIsRefused(t *testing.T) {
	opts := extractOpts(extractBody)
	opts.Offset = -1
	_, err := RunRecipe(context.Background(), extractSourceCaller(4), "doc", kgtypes.GraphWebRaw, opts)
	require.Error(t, err, "a negative cursor names no page a caller could have intended")
	assert.Contains(t, err.Error(), "-1", "the refusal names the offending value")
}
