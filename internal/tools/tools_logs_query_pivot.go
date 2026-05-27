// SPDX-License-Identifier: Apache-2.0

// Package tools — log graph pivot mode.
//
// The pivot mode renders a row×column matrix of log counts keyed by two
// label keys, collapsing the eight marginal distributions the overview
// returns into a single crosswalk. Typical use:
//
//	query({ graph: "logs", name: "<id>", mode: "pivot",
//	        rows: "reporting_instance", cols: "reason" })
//
// Row and column values are sorted by descending total count so the
// dominant concentration surfaces at the top-left. Empty cells render as
// "·" so sparse matrices stay scannable.
package tools

import (
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/logs"
)

// handleLogsPivot renders engine.Pivot() as a markdown matrix. rowKey and
// colKey come from the queryArgs.Rows / queryArgs.Cols top-level fields.
// When either is empty we fall back to sniffing a sensible default from
// the indexed label keys.
func handleLogsPivot(queryID string, engine *logs.QueryEngine, rowKey, colKey string) kgtools.ToolResult {
	rowKey, colKey, err := resolvePivotKeys(engine, rowKey, colKey)
	if err != nil {
		return kgtools.ErrorResult(fmt.Sprintf("logs pivot %q: %s", queryID, err.Error()))
	}
	result := engine.Pivot(rowKey, colKey)
	return kgtools.TextResult(formatLogsPivot(queryID, result))
}

// resolvePivotKeys validates or sniffs defaults for the row/col label keys.
// Sniffing strategy:
//
//   - K8s events: prefer (reporting_instance, reason). These two labels
//     together explain "which node reported which event type" which is
//     the single highest-signal cut for a node-startup cascade.
//   - Fallback: the two label keys with the most observed values. Keys
//     with only one value are useless for a pivot (one row or one col),
//     so they're skipped.
//
// If the caller specified one key but not the other, we only sniff the
// missing side.
func resolvePivotKeys(engine *logs.QueryEngine, rowKey, colKey string) (string, string, error) {
	if rowKey != "" && colKey != "" {
		if rowKey == colKey {
			return "", "", fmt.Errorf("rows and cols must differ (both were %q)", rowKey)
		}
		return rowKey, colKey, nil
	}
	defaults := sniffPivotDefaults(engine)
	if rowKey == "" {
		rowKey = defaults[0]
	}
	if colKey == "" {
		colKey = defaults[1]
	}
	if rowKey == "" || colKey == "" {
		return "", "", fmt.Errorf(
			"could not sniff pivot defaults — pass rows=<key> cols=<key> explicitly " +
				"(the graph has fewer than two label keys with multiple values)")
	}
	if rowKey == colKey {
		return "", "", fmt.Errorf("rows and cols must differ (both resolved to %q)", rowKey)
	}
	return rowKey, colKey, nil
}

// sniffPivotDefaults picks two high-signal label keys for the pivot.
// Priority order: the K8s events default (reporting_instance, reason)
// when both are present, then the two keys with the most distinct values.
// Returns up to two keys — empty strings when not enough candidates.
func sniffPivotDefaults(engine *logs.QueryEngine) [2]string {
	overview := engine.Overview()
	if _, hasInst := overview["reporting_instance"]; hasInst {
		if _, hasReason := overview["reason"]; hasReason {
			return [2]string{"reporting_instance", "reason"}
		}
	}
	type keyCard struct {
		key  string
		card int
	}
	var ranked []keyCard
	for k, vs := range overview {
		if len(vs) < 2 {
			continue
		}
		ranked = append(ranked, keyCard{key: k, card: len(vs)})
	}
	// Stable sort: more values first, then alphabetical tiebreak.
	for i := 0; i < len(ranked); i++ {
		for j := i + 1; j < len(ranked); j++ {
			if ranked[j].card > ranked[i].card ||
				(ranked[j].card == ranked[i].card && ranked[j].key < ranked[i].key) {
				ranked[i], ranked[j] = ranked[j], ranked[i]
			}
		}
	}
	var out [2]string
	if len(ranked) > 0 {
		out[0] = ranked[0].key
	}
	if len(ranked) > 1 {
		out[1] = ranked[1].key
	}
	return out
}

// formatLogsPivot renders a PivotResult as a markdown table. Cells show
// "total (err)" when error count is nonzero, else just "total". Empty
// cells render as "·". Rows and columns are capped at pivotCap with an
// "…and N more" line when exceeded.
func formatLogsPivot(queryID string, r *logs.PivotResult) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "## Log pivot — %s\n\n", queryID)
	fmt.Fprintf(&sb, "**rows:** `%s` × **cols:** `%s`\n\n", r.RowKey, r.ColKey)
	fmt.Fprintf(&sb,
		"%d cell(s) populated · %d stream(s) covered · %d skipped (missing one of the pivot keys)\n\n",
		countCells(r), r.StreamsCovered, r.StreamsSkipped)
	if len(r.Rows) == 0 || len(r.Cols) == 0 {
		sb.WriteString("_No streams carry both pivot keys. Try different `rows` / `cols` values._\n")
		return sb.String()
	}

	rows := capPivotKeys(r.Rows)
	cols := capPivotKeys(r.Cols)

	writePivotHeader(&sb, r.ColKey, cols)
	writePivotBody(&sb, r, rows, cols)
	writePivotFooter(&sb, r, cols)
	writePivotOverflow(&sb, r, len(rows), len(cols))
	return sb.String()
}

// capPivotKeys returns at most maxPivotKeys keys, preserving order.
func capPivotKeys(keys []string) []string {
	const maxPivotKeys = 20
	if len(keys) <= maxPivotKeys {
		return keys
	}
	return keys[:maxPivotKeys]
}

// writePivotHeader renders the header row "| colKey \ N | col1 | col2 | ... | total |".
func writePivotHeader(sb *strings.Builder, colKey string, cols []string) {
	fmt.Fprintf(sb, "| %s ↓ / %s → |", "row", colKey)
	for _, c := range cols {
		fmt.Fprintf(sb, " %s |", c)
	}
	sb.WriteString(" **total** |\n")
	sb.WriteString("|---|")
	for range cols {
		sb.WriteString("---|")
	}
	sb.WriteString("---|\n")
}

// writePivotBody renders one row per row-value with "total (err)" cells.
func writePivotBody(sb *strings.Builder, r *logs.PivotResult, rows, cols []string) {
	for _, rv := range rows {
		fmt.Fprintf(sb, "| `%s` |", rv)
		for _, cv := range cols {
			sb.WriteString(" ")
			sb.WriteString(renderPivotCell(r.Cells[rv][cv]))
			sb.WriteString(" |")
		}
		fmt.Fprintf(sb, " **%s** |\n", renderPivotCell(r.RowTotals[rv]))
	}
}

// writePivotFooter renders the totals row at the bottom of the table.
func writePivotFooter(sb *strings.Builder, r *logs.PivotResult, cols []string) {
	sb.WriteString("| **total** |")
	for _, cv := range cols {
		fmt.Fprintf(sb, " **%s** |", renderPivotCell(r.ColTotals[cv]))
	}
	fmt.Fprintf(sb, " **%s** |\n", renderPivotCell(&r.Grand))
}

// writePivotOverflow notes how many rows/cols were dropped by the cap.
func writePivotOverflow(sb *strings.Builder, r *logs.PivotResult, shownRows, shownCols int) {
	if len(r.Rows) > shownRows {
		fmt.Fprintf(sb, "\n…and %d more row(s) below the top %d by total.\n",
			len(r.Rows)-shownRows, shownRows)
	}
	if len(r.Cols) > shownCols {
		fmt.Fprintf(sb, "\n…and %d more column(s) below the top %d by total.\n",
			len(r.Cols)-shownCols, shownCols)
	}
}

// renderPivotCell renders a single cell as "N" when no errors, or "N (E err)"
// when errors > 0. Nil/empty cells render as "·" so sparse matrices don't
// look like zero-filled ones.
func renderPivotCell(c *logs.PivotCell) string {
	if c == nil || c.TotalCount == 0 {
		return "·"
	}
	if c.ErrorCount == 0 {
		return fmt.Sprintf("%d", c.TotalCount)
	}
	return fmt.Sprintf("%d (%d err)", c.TotalCount, c.ErrorCount)
}

// countCells returns the number of populated cells in the matrix.
func countCells(r *logs.PivotResult) int {
	n := 0
	for _, row := range r.Cells {
		n += len(row)
	}
	return n
}
