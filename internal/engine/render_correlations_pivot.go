// SPDX-License-Identifier: Apache-2.0

package engine

import (
	"fmt"
	"sort"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// render_correlations_pivot.go ports the GENERIC (graph-neutral) correlations
// and pivot renderers + the pivot-matrix builder the
// InterceptQueryCorrelationsPivot composer (cmd/knowledge/internal/tools)
// consumes. Direct ports of the server formatGenericCorrelations
// (tools_query_correlations.go) and formatPivotMatrix + buildPivotMatrix +
// extractNodeField (tools_query_pivot.go / tools_query_pivot_render.go). The
// logs path fills the SAME PivotMatrix shape and reuses these same generic
// writers — porting the generic family covers the logs structural shape too,
// but this composer drives only the non-logs path (logs is owned by
// InterceptLogsQuery earlier in the chain).

// The correlations bounds. CorrelationsEdgeScanCap bounds the WORK (edges the
// composer asks the server for); the row caps bound the OUTPUT (rows rendered).
// They are distinct knobs and must not be conflated.
const (
	// CorrelationsRowCapDefault is the rendered row count when the caller passes
	// no limit; 100 rows measured at 14,437 bytes.
	CorrelationsRowCapDefault = 100
	// CorrelationsRowCapMax is the ceiling on a caller-supplied limit.
	CorrelationsRowCapMax = 1000
	// CorrelationsEdgeScanCap is the number of edges the composer will ask the
	// server for — ~5 MB at the measured ~99 bytes/edge, against 20.4 MB for all
	// 216,109 edges of the production knowledge graph.
	CorrelationsEdgeScanCap = 50000
)

// CorrelationEdgeRow pairs an edge with its resolved from/to names + types — the
// row shape RenderCorrelations consumes. The composer builds these from the
// bulk RETURN_MODE_EDGES decode + the node-set name map.
type CorrelationEdgeRow struct {
	Edge     knowledgev1.Edge
	FromName string
	ToName   string
	FromType kgtypes.NodeType
	ToType   kgtypes.NodeType
}

// RenderCorrelations ports the server formatGenericCorrelations: the
// "## Correlations — <label>" header + the from/edge/to/confidence/method table.
// The composer sorts the rows by confidence desc (fromID,toID tiebreak) and caps
// them before calling this (matching handleGenericCorrelations).
//
// rows is the already-sorted, already-capped slice; total is the count BEFORE
// capping, so the header never under-reports what was seen; scanCapped says the
// server truncated the scan. The two notices are independent — a small graph
// hits neither, a filtered call may hit only the truncation notice, an
// unfiltered whole-graph call hits both.
//
// groups carries the multi-candidate groups the composer reconstructed; their
// rows are removed from rows and the group blocks render BELOW the table. total
// and scanCapped are UNCHANGED by grouping — total keeps counting EDGES, group
// members included, because it counts edges rather than asserting call facts and
// three existing behaviors read that number.
//
// Correlations is a ranked whole-graph edge TABLE, not a walk, so no frontier
// line is emitted: there is no path to short-circuit and no distance to truncate.
func RenderCorrelations(label string, rows []CorrelationEdgeRow, total int, scanCapped bool, groups []CandidateGroup) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "## Correlations — %s\n\n", label)
	fmt.Fprintf(&sb, "%d edge(s), sorted by confidence desc.\n\n", total)
	sb.WriteString("| from | edge | to | confidence | method |\n")
	sb.WriteString("|---|---|---|---|---|\n")
	for i := range rows {
		r := &rows[i]
		fromLabel := fmt.Sprintf("`%s` [%s]", r.FromName, typeOrDash(r.FromType))
		toLabel := fmt.Sprintf("`%s` [%s]", r.ToName, typeOrDash(r.ToType))
		// The METHOD half of the group-suppression rule: a grouped row's method
		// belongs to its block, which states it as the block's semantics. This
		// renderer prints no Evidence column, so it needs only this half. Guard,
		// not removal — every non-group row still shows its method. Belt-and-braces
		// for the same reason as the explain arm: composers pass only ungrouped
		// rows, but this is a primitive a future surface may call unfiltered.
		method := methodOrDash(r.Edge.Method)
		if IsCandidateEdge(&r.Edge) {
			method = methodOrDash("")
		}
		fmt.Fprintf(&sb, "| %s | %s | %s | %.2f | %s |\n",
			fromLabel, r.Edge.Type, toLabel, r.Edge.Confidence, method)
	}
	if total > len(rows) {
		fmt.Fprintf(&sb, "\n…and %d more edge(s) below the top %d by confidence.\n", total-len(rows), len(rows))
	}
	if scanCapped {
		// SAMPLE, not "the first N": the server's match-all edge walk ranges an
		// *xsync.Map whose Range order is unspecified, so a truncated scan yields
		// an arbitrary, non-reproducible subset. Presenting a top-N over that as
		// the graph's true top-N would be a false claim, and the caller has a real
		// remedy — edge_type is pushed server-side, so a filtered call is usually
		// exact.
		fmt.Fprintf(&sb,
			"\n_Scan capped at %d edge(s) of an UNORDERED walk — this ranking is a SAMPLE, not the graph's true top %d. Narrow with edge_type for an exact ranking._\n",
			total, len(rows))
	}
	writeCandidateGroupsNoFrontier(&sb, groups, nil, nil)
	return sb.String()
}

// RenderCorrelationsEmpty renders the no-edges body matching
// handleGenericCorrelations' empty branch.
func RenderCorrelationsEmpty(label, filterMsg string) string {
	return fmt.Sprintf("## Correlations — %s\n\n_No edges found for filter: %s._\n", label, filterMsg)
}

// CorrelationNodeName mirrors the server correlationNodeName: SymbolName → ID.
func CorrelationNodeName(n *knowledgev1.Node) string {
	if n.SymbolName != "" {
		return n.SymbolName
	}
	return n.Id
}

func typeOrDash(t kgtypes.NodeType) string {
	if t == "" {
		return "-"
	}
	return string(t)
}

func methodOrDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// PivotMatrix is the graph-neutral pivot payload — a direct port of the server
// pivotMatrix (tools_query_pivot_render.go). StreamsCovered/StreamsSkipped stay
// zero off the logs path.
type PivotMatrix struct {
	RowKey         string
	ColKey         string
	Rows           []string
	Cols           []string
	Cells          map[string]map[string]int
	RowTotals      map[string]int
	ColTotals      map[string]int
	Grand          int
	StreamsCovered int
	StreamsSkipped int
}

// PivotAccumulator folds nodes into a PivotMatrix one PAGE at a time, so a pivot
// over a whole graph never materializes the node set — the counts are all the
// matrix needs, and they are O(distinct row×col keys) rather than O(corpus).
type PivotAccumulator struct {
	m PivotMatrix
}

// NewPivotAccumulator starts an empty accumulation keyed on the two pivot fields.
func NewPivotAccumulator(rowField, colField string) *PivotAccumulator {
	return &PivotAccumulator{m: PivotMatrix{
		RowKey:    rowField,
		ColKey:    colField,
		Cells:     make(map[string]map[string]int),
		RowTotals: make(map[string]int),
		ColTotals: make(map[string]int),
	}}
}

// Add folds one page of nodes in, skipping any node missing either pivot key.
func (a *PivotAccumulator) Add(nodes []*knowledgev1.Node) {
	for i := range nodes {
		rv, okR := ExtractNodeField(nodes[i], a.m.RowKey)
		cv, okC := ExtractNodeField(nodes[i], a.m.ColKey)
		if !okR || !okC {
			continue
		}
		if a.m.Cells[rv] == nil {
			a.m.Cells[rv] = make(map[string]int)
		}
		a.m.Cells[rv][cv]++
		a.m.RowTotals[rv]++
		a.m.ColTotals[cv]++
		a.m.Grand++
	}
}

// Finish sorts rows and cols by descending total (alphabetic tiebreak) and
// returns the completed matrix.
func (a *PivotAccumulator) Finish() PivotMatrix {
	a.m.Rows = pivotSortedByTotalDesc(a.m.RowTotals)
	a.m.Cols = pivotSortedByTotalDesc(a.m.ColTotals)
	return a.m
}

// BuildPivotMatrix is the whole-slice shape of the accumulator, kept for callers
// that already hold every node (the bounded text-seed arm, the renderer golden).
// Port of the server buildPivotMatrix.
func BuildPivotMatrix(nodes []*knowledgev1.Node, rowField, colField string) PivotMatrix {
	acc := NewPivotAccumulator(rowField, colField)
	acc.Add(nodes)
	return acc.Finish()
}

func pivotSortedByTotalDesc(totals map[string]int) []string {
	keys := make([]string, 0, len(totals))
	for k := range totals {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if totals[keys[i]] != totals[keys[j]] {
			return totals[keys[i]] > totals[keys[j]]
		}
		return keys[i] < keys[j]
	})
	return keys
}

// ExtractNodeField reads a field from a node — port of the server
// extractNodeField. Struct fields (case-sensitive) Type/Status/Source/Language/
// FilePath/SymbolName/IsTest/TestKind; any other name is a metadata key. Empty
// value = "missing" (skip the node) except is_test where bool false is a real
// bucket.
func ExtractNodeField(n *knowledgev1.Node, field string) (string, bool) {
	switch field {
	case "type":
		return pivotStringOK(n.Type)
	case "status":
		return pivotStringOK(n.Status)
	case "source":
		return pivotStringOK(n.Source)
	case "language":
		return pivotStringOK(n.Language)
	case "file_path":
		return pivotStringOK(n.FilePath)
	case "symbol_name":
		return pivotStringOK(n.SymbolName)
	case "is_test":
		if n.IsTest {
			return "true", true
		}
		return "false", true
	case "test_kind":
		return pivotStringOK(n.TestKind)
	default:
		return pivotStringOK(kgtypes.Value(n, field))
	}
}

func pivotStringOK(s string) (string, bool) {
	if s == "" {
		return "", false
	}
	return s, true
}

// RenderPivotMatrix ports the server formatPivotMatrix: the "## Pivot — <label>"
// header + the row×col table + footer + overflow notes.
func RenderPivotMatrix(label string, m PivotMatrix) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "## Pivot — %s\n\n", label)
	fmt.Fprintf(&sb, "**rows:** `%s` × **cols:** `%s`\n\n", m.RowKey, m.ColKey)
	if m.StreamsCovered > 0 || m.StreamsSkipped > 0 {
		fmt.Fprintf(&sb,
			"%d cell(s) populated · %d stream(s) covered · %d skipped (missing one of the pivot keys)\n\n",
			countMatrixCells(m), m.StreamsCovered, m.StreamsSkipped)
	} else {
		fmt.Fprintf(&sb, "%d cell(s) populated · %d total event(s)\n\n", countMatrixCells(m), m.Grand)
	}
	if len(m.Rows) == 0 || len(m.Cols) == 0 {
		sb.WriteString("_No nodes carry both pivot keys._\n")
		return sb.String()
	}
	rows := capPivotKeys(m.Rows)
	cols := capPivotKeys(m.Cols)
	writeGenericPivotHeader(&sb, m.ColKey, cols)
	writeGenericPivotBody(&sb, m, rows, cols)
	writeGenericPivotFooter(&sb, m, cols)
	writeGenericPivotOverflow(&sb, m, len(rows), len(cols))
	return sb.String()
}

func countMatrixCells(m PivotMatrix) int {
	n := 0
	for _, row := range m.Cells {
		n += len(row)
	}
	return n
}

func writeGenericPivotHeader(sb *strings.Builder, colKey string, cols []string) {
	fmt.Fprintf(sb, "| row ↓ / %s → |", colKey)
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

func writeGenericPivotBody(sb *strings.Builder, m PivotMatrix, rows, cols []string) {
	for _, rv := range rows {
		fmt.Fprintf(sb, "| `%s` |", rv)
		for _, cv := range cols {
			fmt.Fprintf(sb, " %s |", renderGenericCell(m.Cells[rv][cv]))
		}
		fmt.Fprintf(sb, " **%d** |\n", m.RowTotals[rv])
	}
}

func writeGenericPivotFooter(sb *strings.Builder, m PivotMatrix, cols []string) {
	sb.WriteString("| **total** |")
	for _, cv := range cols {
		fmt.Fprintf(sb, " **%d** |", m.ColTotals[cv])
	}
	fmt.Fprintf(sb, " **%d** |\n", m.Grand)
}

func writeGenericPivotOverflow(sb *strings.Builder, m PivotMatrix, shownRows, shownCols int) {
	if len(m.Rows) > shownRows {
		fmt.Fprintf(sb, "\n…and %d more row(s) below the top %d by total.\n", len(m.Rows)-shownRows, shownRows)
	}
	if len(m.Cols) > shownCols {
		fmt.Fprintf(sb, "\n…and %d more column(s) below the top %d by total.\n", len(m.Cols)-shownCols, shownCols)
	}
}

func renderGenericCell(n int) string {
	if n == 0 {
		return "·"
	}
	return fmt.Sprintf("%d", n)
}

func capPivotKeys(keys []string) []string {
	const maxPivotKeys = 20
	if len(keys) <= maxPivotKeys {
		return keys
	}
	return keys[:maxPivotKeys]
}
