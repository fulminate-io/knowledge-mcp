// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
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
	return collectArgs{Type: "web", ID: "hohpe-eip", Transformer: "recipe", RecipeBody: "select section", Extract: true, MaxRows: maxRows, MaxBytes: maxBytes}
}

// TestRenderExtract_TruncationIsDisclosed covers all four states. The
// under-both-caps case is what stops a renderer that ALWAYS prints the
// truncation line from passing; zero_rows_fit is the "no silent caps"
// requirement in its worst case.
func TestRenderExtract_TruncationIsDisclosed(t *testing.T) {
	const disclosure = "TRUNCATED by"

	t.Run("under_both_caps", func(t *testing.T) {
		out := renderExtract(extractArgs(0, 0), extractFixture(3, 3, ""))
		assert.Contains(t, out, "extract: recipe=inline source=web/hohpe-eip")
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

// TestRenderExtract_DisclosesEveryStat covers the disclosure the extract header
// used to omit entirely, in both directions.
func TestRenderExtract_DisclosesEveryStat(t *testing.T) {
	t.Run("every_counter_is_readable", func(t *testing.T) {
		// A NON-ZERO value in every disclosed counter, all distinct, so a renderer
		// printing literal zeroes, or disclosing only the skipped count, fails.
		res := extractFixture(2, 2, "")
		res.Stats = recipe.Stats{
			SkippedChunks: 7, LookupsResolved: 3, LookupMisses: 5,
			LinkMisses: 11, ElapsedMillis: 42,
		}
		out := renderExtract(extractArgs(0, 0), res)
		assert.Contains(t, out, "skipped=7")
		assert.Contains(t, out, "lookups_resolved=3")
		assert.Contains(t, out, "lookup_misses=5")
		assert.Contains(t, out, "link_misses=11")
		assert.Contains(t, out, "elapsed_ms=42")
	})

	t.Run("matched_nothing_versus_skipped_everything", func(t *testing.T) {
		// BOTH STATES ARE DRIVEN THROUGH THE REAL INTERPRETER, and that is the
		// point of this subtest rather than an implementation detail. Measured: a
		// fixture-based version, handing a non-nil ExtractResult to both renders,
		// reports the two as DIFFERENT and is GREEN against a build where the real
		// path is still byte-identical — because the shape the real path produces
		// on the skipped case is a NIL Extract, which a fixture never supplies. The
		// fixture would decide the verdict instead of the code.
		run := func(t *testing.T, body string) string {
			t.Helper()
			caller := extractStateCaller()
			res, err := recipe.RunRecipe(context.Background(), caller, "src-graph",
				kgtypes.GraphWebRaw, recipe.Options{
					SourceManifest: recipe.FormatSourceManifest("hohpe-eip", "inline"),
					Body:           body,
					Extract:        true,
				})
			require.NoError(t, err)
			return renderExtract(collectArgs{
				Type: "web", ID: "hohpe-eip", Transformer: "recipe", Extract: true, RecipeBody: body,
			}, res)
		}

		matchedNothing := run(t, `select section
filter {"equals": {"of": "section.symbol_name", "value": "no section is named this"}}
emit pattern {
    name := section.symbol_name
}`)
		skippedEverything := run(t, `select section
emit pattern {
    name := section.metadata.blank
}`)

		assert.NotEqual(t, matchedNothing, skippedEverything,
			"a reader must be able to tell an extract that matched nothing from one whose every row was skipped")
		assert.Contains(t, matchedNothing, "rows=0/0")
		assert.Contains(t, matchedNothing, "skipped=0")
		assert.Contains(t, skippedEverything, "skipped=2",
			"both rows resolved an empty identity and were skipped")
	})
}

// extractStateCaller serves two sections carrying an EMPTY `blank` metadata key.
// The key is stamped so the source census admits a recipe that reads it; its
// value is empty so the emit's identity resolves empty on every row, which is
// the skipped-everything state.
func extractStateCaller() *recipeRoutingCaller {
	return &recipeRoutingCaller{
		nodesByGraph: map[string][]*knowledgev1.Node{
			string(kgtypes.GraphWebRaw): {
				{Id: "s1", Type: "section", SymbolName: "Message Router", Metadata: map[string]string{"blank": ""}},
				{Id: "s2", Type: "section", SymbolName: "Message Channel", Metadata: map[string]string{"blank": ""}},
			},
		},
		edgesByGraph: map[string][]*knowledgev1.Edge{},
	}
}

// TestRecipeRunOptions_RefusesParams pins all three mandated message properties
// plus the control that rejects a refusal firing unconditionally.
func TestRecipeRunOptions_RefusesParams(t *testing.T) {
	base := collectArgs{Type: "web", ID: "hohpe-eip", Transformer: "recipe", RecipeBody: "select section"}

	t.Run("refuses_a_supplied_params_object", func(t *testing.T) {
		a := base
		a.Params = map[string]any{"depth": 3}
		_, err := recipeRunOptions(a)
		require.Error(t, err)
		msg := err.Error()
		assert.Contains(t, msg, "params", "(a) the offending param is named")
		assert.Contains(t, msg, "bind $x", "(b) the redirect says what to do instead")
		assert.NotContains(t, msg, "not yet supported",
			"(c) the refusal is PERMANENT: a not-yet phrasing is a roadmap promise nothing backs")
	})

	t.Run("empty_params_still_builds", func(t *testing.T) {
		// THE CONTROL. A refusal that fired unconditionally would break every
		// existing recipe call, and would pass the subtest above.
		for name, a := range map[string]collectArgs{
			"absent": base,
			"empty":  func() collectArgs { c := base; c.Params = map[string]any{}; return c }(),
		} {
			t.Run(name, func(t *testing.T) {
				opts, err := recipeRunOptions(a)
				require.NoError(t, err)
				assert.Equal(t, "inline", opts.SourceManifest[len(opts.SourceManifest)-6:],
					"options are built normally, under the one fixed inline recipe key")
			})
		}
	})
}
