// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// collect_recipe_offset_test.go covers the collect surface's half of the extract
// cursor: the two disclosures the renderer prints, and the declared-versus-
// consumed partition that keeps `offset` from being accepted where nothing reads
// it.

// offsetArgs is extractArgs plus a cursor.
func offsetArgs(maxRows, maxBytes, offset int) collectArgs {
	a := extractArgs(maxRows, maxBytes)
	a.Offset = offset
	return a
}

func TestRenderExtract_CursorIsDisclosed(t *testing.T) {
	const overshoot = "OFFSET PAST END:"

	// THE TWO STAMPERS DISAGREE HERE ON PURPOSE. The interpreter captured six
	// rows; the byte cap lets the renderer keep three. Resuming from the
	// interpreter's count would skip the three rows the byte cap cut and the
	// caller never saw.
	t.Run("next_offset_counts_rendered_rows_not_interpreted_ones", func(t *testing.T) {
		res := extractFixture(6, 40, "max_rows")
		out := renderExtract(offsetArgs(6, 3*rowRenderBytes(t), 10), res)
		assert.Contains(t, out, "Next offset=13.",
			"10 supplied + 3 rendered — not 10 + the 6 the interpreter captured")
		assert.NotContains(t, out, "Next offset=16.")
	})

	t.Run("offset_past_the_end_is_named", func(t *testing.T) {
		out := renderExtract(offsetArgs(10, 0, 100), extractFixture(0, 40, ""))
		assert.Contains(t, out, overshoot)
		assert.Contains(t, out, "offset=100 starts after the 40 matched row(s)")
		assert.Contains(t, out, "the last page starts at offset=30",
			"forty rows at ten per page: the last real page starts at 30")
	})

	t.Run("a_complete_first_page_says_nothing_about_cursors", func(t *testing.T) {
		out := renderExtract(offsetArgs(0, 0, 0), extractFixture(3, 3, ""))
		assert.NotContains(t, out, overshoot)
		assert.NotContains(t, out, "Next offset=")
	})

	// THE FOURTH CONDITION. A page the BYTE cap emptied is not a cursor
	// overshoot: the offset is not past the matched rows, the cursor did not
	// empty the page, and naming offset=0 as the last page would send the caller
	// back to page one.
	t.Run("a_byte_capped_page_names_the_byte_cap_not_the_cursor", func(t *testing.T) {
		out := renderExtract(offsetArgs(0, 20, 10), extractFixture(6, 40, "max_rows"))
		assert.Contains(t, out, "TRUNCATED by max_bytes=20")
		assert.NotContains(t, out, overshoot,
			"the byte cap emptied this page, not the cursor")
	})

	t.Run("genuinely_empty_is_not_reported_as_a_cursor_overshoot", func(t *testing.T) {
		out := renderExtract(offsetArgs(0, 0, 5), extractFixture(0, 0, ""))
		assert.NotContains(t, out, overshoot,
			"nothing matched at all — there is no population the cursor ran past")
	})
}

// rowRenderBytes measures one fixture row's rendered size, so a byte cap can be
// set to admit an exact number of rows rather than a guessed constant.
func rowRenderBytes(t *testing.T) int {
	t.Helper()
	out := renderExtract(extractArgs(0, 0), extractFixture(1, 1, ""))
	i := strings.Index(out, "--- row 0 ")
	require.GreaterOrEqual(t, i, 0, "the rendered output must contain a row")
	return len(out) - i
}

// TestRejectRecipeOnlyArgs_RefusesOffsetByName covers BOTH refusal arms and BOTH
// admission controls. The fourth subtest is the one that separates a predicate
// entry in the `set` map from an unconditional `true`: the unconditional form
// compiles, passes the other three, and refuses every plain collect.
func TestRejectRecipeOnlyArgs_RefusesOffsetByName(t *testing.T) {
	t.Run("non_recipe_transformer", func(t *testing.T) {
		err := rejectRecipeOnlyArgs(collectArgs{Type: "pdf", Transformer: "", Offset: 5})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "offset", "the refusal names the param")
	})

	t.Run("non_web_or_pdf_type", func(t *testing.T) {
		err := rejectRecipeOnlyArgs(collectArgs{Type: "code", Transformer: "recipe", Offset: 5})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "offset")
	})

	t.Run("a_recipe_extract_on_pdf_is_admitted", func(t *testing.T) {
		assert.NoError(t, rejectRecipeOnlyArgs(collectArgs{
			Type: "pdf", Transformer: "recipe", Extract: true, Offset: 5,
		}))
	})

	// AN UNSUPPLIED OFFSET IS NOT A SUPPLIED PARAM. Written as an unconditional
	// `true` in the set map this refuses the code collector and every plain
	// collect by name.
	t.Run("offset_zero_is_not_a_supplied_param", func(t *testing.T) {
		assert.NoError(t, rejectRecipeOnlyArgs(collectArgs{Type: "code"}),
			"a plain collect supplies no recipe-only param and must not be refused")
	})
}
