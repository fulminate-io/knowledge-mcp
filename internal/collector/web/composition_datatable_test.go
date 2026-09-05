// SPDX-License-Identifier: Apache-2.0

package web

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector"
)

// TestCollect_DataTableOnlyHarvest_IsSubstantiveButChromeStillFails drives the
// captured-only-chrome invariant through A REAL CRAWL in BOTH directions, and
// the pair is the point.
//
// WHY IT HAD TO CHANGE. This ticket made a data table's cells the SOLE carrier
// of their text: the page-level flatten is retired, and emitTable writes the
// rendered cells into Content because no other node carries them. The harvest
// invariant still summed paragraph plus code_block only, so a crawl of a
// table-dominant site — spec tables, label/value grids, API reference matrices
// — was refused outright as "captured only chrome" while its text sat in the
// graph. The refusal discards a good harvest, which is worse than the guard is
// worth.
//
// WHY WIDENING NEEDS THE SECOND LEG. A guard widened until nothing fails it is
// not a guard. The chrome control below is a LAYOUT table: layout tables are
// walked into their own records and emitTable deliberately writes them NO
// Content, so counting them as substantive would let a page of pure table
// scaffolding read as a good harvest. The two legs move in opposite directions
// under the same change, so neither an unwidened sum nor an indiscriminate one
// can be green.
func TestCollect_DataTableOnlyHarvest_IsSubstantiveButChromeStillFails(t *testing.T) {
	t.Run("a_data_table_is_substantive_content", func(t *testing.T) {
		const dataTableOnlyHTML = `<!doctype html>
<html>
<head><title>Weakness Enumeration</title></head>
<body><article>
<h1>Weakness Enumeration</h1>
<table>
<thead><tr><th>Identifier</th><th>Name</th><th>Likelihood</th></tr></thead>
<tbody>
<tr><td>CWE-79</td><td>Improper Neutralization of Input</td><td>High</td></tr>
<tr><td>CWE-89</td><td>Improper Neutralization of Special Elements</td><td>High</td></tr>
<tr><td>CWE-125</td><td>Out-of-bounds Read</td><td>Medium</td></tr>
<tr><td>CWE-416</td><td>Use After Free</td><td>Medium</td></tr>
</tbody>
</table>
</article></body>
</html>
`
		comp := serveComposition(t, "data-table-only", dataTableOnlyHTML)

		// THE PREMISE, asserted so the leg below cannot pass for the wrong
		// reason: this harvest really does carry its text ONLY in table cells.
		assert.Positive(t, comp.NodesByType["page"], "the crawl must emit a page node — that is what arms the page gate")
		assert.Positive(t, comp.NodesByType["table"], "the fixture must emit a table node, or there is nothing to count")
		assert.Zero(t, comp.NodesByType["paragraph"],
			"the fixture must carry NO paragraph, or the invariant would pass on the old sum and this leg would prove nothing: %s", comp.Render())
		assert.Zero(t, comp.NodesByType["code_block"],
			"the fixture must carry NO code_block, for the same reason: %s", comp.Render())

		require.NoError(t, collector.CheckComposition("web", comp),
			"a harvest whose text lives in data-table cells is a good harvest, not chrome: %s", comp.Render())
	})

	// THE CONTROL. A layout table carries no Content of its own — its cells are
	// walked into their own records — so a page of pure table scaffolding must
	// still be refused. Without this leg, "count table nodes" would silence the
	// guard for every chrome page built out of tables, which is most of the
	// pre-CSS web the crawler actually meets.
	//
	// role="presentation" IS THE FIXTURE'S LOAD-BEARING ATTRIBUTE, and it is
	// there because the collector's own verdict says so rather than because it
	// looks like chrome to a reader: classifyTable (parse_dom_blocks.go) makes
	// Layout true for role presentation/none, false on a header signal, false
	// on uniform rows, and otherwise defers to whether a cell holds a block. A
	// single uniform row of anchors is therefore a DATA table by that verdict —
	// measured, after a first draft of this fixture wrote exactly that and read
	// as substantive — so the control has to carry the signal the classifier
	// actually keys on.
	t.Run("a_layout_table_page_still_reads_as_chrome", func(t *testing.T) {
		const layoutTableOnlyHTML = `<!doctype html>
<html>
<head><title>Layout Only</title></head>
<body>
<table role="presentation"><tr>
<td><a href="/one">First Navigation Link</a></td>
<td><a href="/two">Second Navigation Link</a></td>
</tr></table>
</body>
</html>
`
		comp := serveComposition(t, "layout-table-only", layoutTableOnlyHTML)

		assert.Positive(t, comp.NodesByType["page"], "the crawl must emit a page node — that is what arms the page gate")
		// THE PREMISE: the table really is emitted, and really is counted as
		// retained chrome. Without this pair the leg could pass because the
		// table never reached the graph at all, which would prove nothing about
		// the subtraction.
		//
		// THE CHROME COUNT IS PINNED EXACTLY, not asserted positive, because
		// this fixture's two nav anchors also emit two links_only paragraphs
		// that countRetainedChrome counts. A Positive() assertion is satisfied
		// at 2 by those paragraphs alone — that is, in the very state where the
		// layout table was NOT counted, which is the state the message claims
		// to reject. Three is the whole composition: two links_only paragraphs
		// plus the one layout table, so the table's own contribution to the
		// subtraction is what moves this number.
		assert.Positive(t, comp.NodesByType["table"], "the layout table must still be emitted — it is retained, not deleted")
		require.Equal(t, 3, comp.NonSubstantiveNodes,
			"retained chrome must be exactly the two links_only nav paragraphs plus the one layout table — a count of 2 means the collector that knows what a layout table is did not count it: %s", comp.Render())

		err := collector.CheckComposition("web", comp)
		require.Error(t, err,
			"a harvest whose only table is layout scaffolding must not report plain success: %s", comp.Render())
		assert.Contains(t, err.Error(), "harvest captured nothing usable")
	})
}
