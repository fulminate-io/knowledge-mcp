// SPDX-License-Identifier: Apache-2.0

package logs

import (
	"sort"

	wirelogs "github.com/fulminate-io/knowledge-mcp/internal/logwire"
)

// PivotCell holds the aggregated counts for a single (rowValue, colValue)
// bucket in a log graph pivot. TotalCount is the sum of chunk EntryCount
// values for every chunk whose stream carries both the rowKey and colKey
// labels; ErrorCount is the subset whose linked template is at or above
// wirelogs.SeverityError.
type PivotCell struct {
	TotalCount int
	ErrorCount int
}

// PivotResult is a row×column aggregation of log counts, keyed by the
// values of two label keys. Rows and Cols are returned in descending-total
// order so callers can render the highest-signal rows/cols first.
type PivotResult struct {
	RowKey    string
	ColKey    string
	Rows      []string                         // row values ordered by total desc
	Cols      []string                         // col values ordered by total desc
	Cells     map[string]map[string]*PivotCell // row → col → cell
	RowTotals map[string]*PivotCell
	ColTotals map[string]*PivotCell
	Grand     PivotCell

	// StreamsCovered and StreamsSkipped report how many streams contributed
	// to the matrix vs. how many were skipped because they lack one of the
	// two pivot keys. Helps the caller decide whether the chosen pivot keys
	// are a good fit for the graph.
	StreamsCovered int
	StreamsSkipped int
}

// Pivot builds a row×col matrix of log counts using the values of two
// label keys. Streams lacking either label are skipped (and counted in
// StreamsSkipped). The grand total and per-row / per-col marginals are
// computed alongside so the caller can render totals without a second
// pass.
func (qe *QueryEngine) Pivot(rowKey, colKey string) *PivotResult {
	res := &PivotResult{
		RowKey:    rowKey,
		ColKey:    colKey,
		Cells:     make(map[string]map[string]*PivotCell),
		RowTotals: make(map[string]*PivotCell),
		ColTotals: make(map[string]*PivotCell),
	}
	for _, s := range qe.streamByID {
		rowVal, hasRow := s.Labels[rowKey]
		colVal, hasCol := s.Labels[colKey]
		if !hasRow || !hasCol {
			res.StreamsSkipped++
			continue
		}
		res.StreamsCovered++
		for _, c := range qe.chunksByStream[s.ID] {
			tmpl := qe.templateByID[c.TemplateID]
			if tmpl == nil {
				continue
			}
			isErr := wirelogs.SeverityAtLeast(tmpl.Severity, wirelogs.SeverityError)
			addPivotCell(res, rowVal, colVal, c.EntryCount, isErr)
		}
	}
	res.Rows = sortedPivotKeys(res.RowTotals)
	res.Cols = sortedPivotKeys(res.ColTotals)
	return res
}

// addPivotCell mutates the matrix, row/col totals, and grand total for a
// single chunk contribution. Factored out so Pivot's outer loop stays
// short enough for the complexity linter.
func addPivotCell(res *PivotResult, rowVal, colVal string, count int, isErr bool) {
	row, ok := res.Cells[rowVal]
	if !ok {
		row = make(map[string]*PivotCell)
		res.Cells[rowVal] = row
	}
	cell, ok := row[colVal]
	if !ok {
		cell = &PivotCell{}
		row[colVal] = cell
	}
	cell.TotalCount += count
	rt := getOrInitPivotCell(res.RowTotals, rowVal)
	ct := getOrInitPivotCell(res.ColTotals, colVal)
	rt.TotalCount += count
	ct.TotalCount += count
	res.Grand.TotalCount += count
	if isErr {
		cell.ErrorCount += count
		rt.ErrorCount += count
		ct.ErrorCount += count
		res.Grand.ErrorCount += count
	}
}

// getOrInitPivotCell returns the cell for key, creating a zero cell if
// missing. Used for row/col marginals.
func getOrInitPivotCell(m map[string]*PivotCell, key string) *PivotCell {
	c, ok := m[key]
	if !ok {
		c = &PivotCell{}
		m[key] = c
	}
	return c
}

// sortedPivotKeys returns the keys of a PivotCell map in descending order
// by TotalCount, with ties broken alphabetically so output is stable.
func sortedPivotKeys(m map[string]*PivotCell) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := m[keys[i]], m[keys[j]]
		if a.TotalCount != b.TotalCount {
			return a.TotalCount > b.TotalCount
		}
		return keys[i] < keys[j]
	})
	return keys
}
