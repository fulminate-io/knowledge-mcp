// SPDX-License-Identifier: Apache-2.0

// style_rule_index_columns_test.go — the index row's WIDTH axis, one subtest per
// column of the table style_rule_index.go declares.
//
// WHY IT IS DRIVEN RATHER THAN DECLARED. The comment table beside styleIndexRow
// names each column capped or uncapped-by-design; a table proves only that its
// own rows agree with each other. This file is the paired check that fails when
// the code does the opposite of a row: every capped row driven with a 4000-byte
// value and asserted bounded, the uncapped row driven with a 4000-byte id and
// asserted whole.
//
// THE WIDTH THAT BROKE THE BOUND WAS THE ONE THE OLD TEST HELD FIXED. The
// declared 200-byte row bound budgeted eight bytes for severity, on the strength
// of the ladder's longest word; but this render deliberately does NOT validate
// severity against the ladder, and a practice node accepts arbitrary metadata,
// so the stored width is corpus data. A read that admits arbitrary VALUES cannot
// assume a bounded WIDTH.

package tools

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// The documented figures as literals. kgtypes' own test pins the same numbers to
// the same literals; nothing here reads the constant it is checking.
const (
	wantIndexColumnCap = 60
	// wantIndexCappedColumns and wantIndexSeparatorBytes are the row bound's two
	// FIXED terms: four capped columns, and four tabs plus one newline.
	wantIndexCappedColumns  = 4
	wantIndexSeparatorBytes = 5
)

// wantIndexRowBound is the row bound's DOCUMENTED arithmetic, written out here
// rather than read from kgtypes: len(id) + four capped columns + the separators.
//
// IT TAKES THE ID BECAUSE THE BOUND DOES. The id is the row's one uncapped
// column, and a bound that budgeted a nominal width for it was false for every
// wider id the corpus admits.
func wantIndexRowBound(id string) int {
	return len(id) + wantIndexCappedColumns*wantIndexColumnCap + wantIndexSeparatorBytes
}

// styleIndexOneRow renders exactly one fixture rule through the real arm and
// returns its five columns.
func styleIndexOneRow(t *testing.T, id, summary string, extra map[string]string) []string {
	t.Helper()
	f := &styleIndexStoreFake{corpus: []*knowledgev1.Node{styleRuleNode(id, summary, extra)}}
	rows := styleIndexRowsOf(t, practiceStyleIndex(opCtx(), f.exec, queryArgs{
		Graph: "practice", Mode: "style_index", Source: "hub-go",
	}))
	require.Len(t, rows, 1)
	cols := strings.Split(rows[0], "\t")
	require.Len(t, cols, 5, "id, severity, scope, summary, sister check")
	return cols
}

// TestPracticeStyleIndex_EveryVariableWidthColumnIsCapped drives each declared
// column at 4000 bytes.
//
// THE MUTATION IS PER COLUMN: drop that one column's capStyleColumn call in
// styleIndexRow and this subtest goes red while the others stay green.
func TestPracticeStyleIndex_EveryVariableWidthColumnIsCapped(t *testing.T) {
	const wide = 4000

	t.Run("severity", func(t *testing.T) {
		cols := styleIndexOneRow(t, "rule-sev", "short", map[string]string{
			"severity": strings.Repeat("W", wide),
		})
		assert.LessOrEqual(t, len(cols[1]), wantIndexColumnCap,
			"an off-ladder severity is rendered AS STORED but its WIDTH is capped")
		assert.True(t, strings.HasSuffix(cols[1], "..."),
			"a capped column says so rather than truncating silently")
	})

	t.Run("scope", func(t *testing.T) {
		cols := styleIndexOneRow(t, "rule-scope", "short", map[string]string{
			kgtypes.MetaKeyStyleScopeRepo: strings.Repeat("r", wide),
		})
		assert.LessOrEqual(t, len(cols[2]), wantIndexColumnCap)
		assert.True(t, strings.HasSuffix(cols[2], "..."))
	})

	t.Run("summary", func(t *testing.T) {
		cols := styleIndexOneRow(t, "rule-summary", strings.Repeat("s", wide), nil)
		assert.LessOrEqual(t, len(cols[3]), wantIndexColumnCap)
		assert.True(t, strings.HasSuffix(cols[3], "..."))
	})

	// THE SISTER-CHECK ID IS CAPPED, and it is the one id in this row that is.
	// A check id admitted through the mutate entry point is caller-supplied and
	// namespaced `<language>:`, so its width is corpus data like any other
	// column — and unlike the ROW's own id it is not what the row exists to
	// resolve: the by-id read of the rule returns it in full.
	t.Run("sister check id", func(t *testing.T) {
		cols := styleIndexOneRow(t, "rule-sister", "short", map[string]string{
			kgtypes.MetaKeySisterCheck: "go:" + strings.Repeat("c", wide),
		})
		assert.LessOrEqual(t, len(cols[4]), wantIndexColumnCap)
		assert.True(t, strings.HasSuffix(cols[4], "..."))
	})

	// THE UNCAPPED-BY-DESIGN ROW and its paired check. A truncated row id does
	// not resolve, and resolving it is the whole point of the row.
	t.Run("the row id is uncapped by design", func(t *testing.T) {
		id := "style-rule-" + strings.Repeat("z", wide)
		cols := styleIndexOneRow(t, id, "short", nil)
		assert.Equal(t, id, cols[0], "the row id is rendered WHOLE")
		assert.NotContains(t, cols[0], "...")
	})
}

// TestPracticeStyleIndex_RowBoundHoldsOverMaximalColumns drives EVERY column of
// the row at its widest SIMULTANEOUSLY, across the id width axis.
//
// WHY THE ID IS AN AXIS AND NOT A FIXTURE VALUE. This test used to hold the id
// at exactly 32 bytes — the width the old constant budgeted — while driving
// every other column at 4000, and called itself the declared bound's check. So
// the one term no cap protected was the one term it never varied, and the bound
// was false at every id width above the budget: 308, 444 and 4244 bytes against
// a declared 280 at ids of 64, 200 and 4000. The bound is a function of the id
// now, and this drives that function's documented arithmetic at each width.
//
// THE MUTATIONS. Budget the id at a fixed 32 in kgtypes.StyleIndexRowBound and
// every row but the 32-byte one goes red. Cap the id in styleIndexRow and the
// whole-id assertion goes red while the bound assertions stay green, which is
// why both live here.
func TestPracticeStyleIndex_RowBoundHoldsOverMaximalColumns(t *testing.T) {
	const wide = 4000
	for _, idWidth := range []int{32, 64, 200, 4000} {
		t.Run(fmt.Sprintf("id of %d bytes", idWidth), func(t *testing.T) {
			id := strings.Repeat("z", idWidth)
			cols := styleIndexOneRow(t, id, strings.Repeat("界", wide), map[string]string{
				"severity":                     strings.Repeat("W", wide),
				kgtypes.MetaKeyStyleScopeRepo:  strings.Repeat("r", wide),
				kgtypes.MetaKeyStyleScopePaths: `["` + strings.Repeat("p", wide) + `"]`,
				kgtypes.MetaKeySisterCheck:     "go:" + strings.Repeat("c", wide),
			})
			row := strings.Join(cols, "\t")

			// THE ID IS RENDERED WHOLE at every width, which is what makes the
			// bound assertions below a statement about a bound rather than about
			// a cap someone quietly added to satisfy them.
			assert.Equal(t, id, cols[0], "the row id is rendered WHOLE at every width")

			want := wantIndexRowBound(id)
			assert.LessOrEqual(t, len(row), want,
				"a row whose every capped column is maximal must fit the DOCUMENTED bound "+
					"for ITS id (%d bytes); got %d", want, len(row))
			assert.Equal(t, want, styleIndexRowBound(id),
				"the production bound must compute the arithmetic its comment documents")
			assert.True(t, styleIndexBlockIsWithinBound([]string{id}, []string{row}),
				"and the production block helper must agree with the documented figure")
		})
	}
}
