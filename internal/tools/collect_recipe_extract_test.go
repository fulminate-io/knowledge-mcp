// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/recipe"
)

// extractFixture builds a result carrying n rendered rows, with rowsMatched
// standing for the full population the rows were drawn from.
func extractFixture(n, rowsMatched int, truncatedBy string) *recipe.Result {
	rows := make([]recipe.ExtractRow, 0, n)
	for i := range n {
		rows = append(rows, recipe.ExtractRow{
			Type:         "pattern",
			SourceNodeID: "s" + strconv.Itoa(i),
			Fields:       map[string]string{"name": "Section " + strconv.Itoa(i)},
		})
	}
	ex := &recipe.ExtractResult{
		Rows: rows, RowsReturned: n, RowsMatched: rowsMatched,
		Truncated: truncatedBy != "", TruncatedBy: truncatedBy,
	}
	return &recipe.Result{Extract: ex}
}

func extractArgs(maxRows, maxBytes int) collectArgs {
	return collectArgs{Type: "web", ID: "hohpe-eip", Transformer: "recipe", Recipe: "eip", Extract: true, MaxRows: maxRows, MaxBytes: maxBytes}
}

// TestRenderExtract_TruncationIsDisclosed covers all four states. The
// under-both-caps case is what stops a renderer that ALWAYS prints the
// truncation line from passing; zero_rows_fit is the "no silent caps"
// requirement in its worst case.
func TestRenderExtract_TruncationIsDisclosed(t *testing.T) {
	const disclosure = "TRUNCATED by"

	t.Run("under_both_caps", func(t *testing.T) {
		out := renderExtract(extractArgs(0, 0), extractFixture(3, 3, ""))
		assert.Contains(t, out, "extract: recipe=eip source=web/hohpe-eip")
		assert.Contains(t, out, "rows=3/3")
		assert.NotContains(t, out, disclosure,
			"a complete extract must carry NO truncation line")
		assert.Contains(t, out, "--- row 0 type=pattern src=s0")
	})

	t.Run("row_cap", func(t *testing.T) {
		// The interpreter already cut 3 of 7 and stamped the reason; the
		// renderer must carry that through with honest counts.
		out := renderExtract(extractArgs(3, 0), extractFixture(3, 7, "max_rows"))
		require.Contains(t, out, disclosure)
		assert.Contains(t, out, "max_rows=3")
		assert.Contains(t, out, "returned 3 of 7 matched row(s)")
		assert.Contains(t, out, "rows=3/7", "the header agrees with the disclosure")
	})

	t.Run("byte_cap", func(t *testing.T) {
		// A cap that admits some rows but not all of them.
		res := extractFixture(6, 6, "")
		full := renderExtract(extractArgs(0, 0), extractFixture(6, 6, ""))
		require.NotContains(t, full, disclosure, "control: uncapped, this fixture is complete")

		out := renderExtract(extractArgs(0, len(full)/2), res)
		require.Contains(t, out, disclosure)
		assert.Contains(t, out, "max_bytes=")
		// THE CUT FELL ON A ROW BOUNDARY: every row header present in the
		// output is followed by its field line, so nothing was clipped
		// mid-value.
		for i := range 6 {
			header := "--- row " + strconv.Itoa(i) + " "
			if !strings.Contains(out, header) {
				continue
			}
			assert.Contains(t, out, "name: Section "+strconv.Itoa(i),
				"row %d was rendered without its field — the cut clipped mid-row", i)
		}
	})

	t.Run("zero_rows_fit", func(t *testing.T) {
		// A cap so small that not even the first row fits. An unqualified empty
		// response here is indistinguishable from a document that matched
		// nothing, so the disclosure must name the first row's size.
		out := renderExtract(extractArgs(0, 10), extractFixture(4, 4, ""))
		require.Contains(t, out, disclosure)
		assert.Contains(t, out, "rows=0/4")
		assert.Contains(t, out, "NO rows fit")
		assert.Regexp(t, `first row alone renders to \d+ bytes`, out,
			"the caller must be told a size that would work")
		assert.NotContains(t, out, "--- row 0", "no partial row may be emitted")
	})
}
