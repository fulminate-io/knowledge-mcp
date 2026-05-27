// SPDX-License-Identifier: Apache-2.0

// Package tools — log graph pivot mode tests.
//
// These exercise handleLogsPivot end-to-end via handleLogsQuery. The
// synthetic entries defined in tools_logs_query_test.go already give us
// two distinct label keys (service, pod) with multiple values, which is
// the minimum needed to exercise the pivot path without shipping a
// second fixture provider.
package tools

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/logs"
	logwire "github.com/fulminate-io/knowledge-mcp/internal/logwire"
)

// TestLogsQuery_PivotExplicitKeys asserts that an explicit (rows, cols)
// pair renders a matrix with the expected header, a row per rowKey value,
// and error annotations where severity>=ERROR.
func TestLogsQuery_PivotExplicitKeys(t *testing.T) {
	queryID := "q-pivot-explicit"
	h := setupLogTestHandler(t, queryID)
	result := h.handleLogsQuery(context.Background(), queryArgs{
		Graph: "logs", Name: queryID, Mode: "pivot",
		Rows: "service", Cols: "pod",
	})
	require.False(t, result.IsError, "pivot: %s", resultText(result))
	text := resultText(result)

	assert.Contains(t, text, "Log pivot", "pivot header should render")
	assert.Contains(t, text, "rows:", "rows metadata should render")
	assert.Contains(t, text, "cols:", "cols metadata should render")
	assert.Contains(t, text, "`service`", "rows key should echo back")
	assert.Contains(t, text, "`pod`", "cols key should echo back")
	// The api service splits across two pods — both should appear in the
	// matrix body as columns.
	assert.Contains(t, text, "api-0", "first api pod should appear as col")
	assert.Contains(t, text, "api-1", "second api pod should appear as col")
	// Totals row should render.
	assert.Contains(t, text, "**total**", "totals row should render")
	// The api service produces ERROR-severity entries — they should be
	// annotated with "(N err)" somewhere in the table.
	assert.Contains(t, text, "err)", "error count annotation should appear")
}

// TestLogsQuery_PivotSniffedDefaults asserts the pivot falls back to a
// sniffed (rowKey, colKey) pair when the caller omits rows/cols. With
// the synthetic fixture the fallback should pick keys that actually
// produce populated cells — not both resolving to the same key.
func TestLogsQuery_PivotSniffedDefaults(t *testing.T) {
	queryID := "q-pivot-sniff"
	h := setupLogTestHandler(t, queryID)
	result := h.handleLogsQuery(context.Background(), queryArgs{
		Graph: "logs", Name: queryID, Mode: "pivot",
	})
	require.False(t, result.IsError, "pivot sniff: %s", resultText(result))
	text := resultText(result)

	assert.Contains(t, text, "Log pivot", "pivot header should render")
	// Should not declare zero cells — at least one populated cell is
	// expected given the synthetic fixture has multiple services and pods.
	assert.NotContains(t, text, "0 cell(s) populated",
		"sniffed defaults must pick keys that produce a populated matrix")
}

// TestLogsQuery_PivotSameKeyRejected asserts the handler rejects
// rows==cols rather than silently rendering a degenerate diagonal.
func TestLogsQuery_PivotSameKeyRejected(t *testing.T) {
	queryID := "q-pivot-samekey"
	h := setupLogTestHandler(t, queryID)
	result := h.handleLogsQuery(context.Background(), queryArgs{
		Graph: "logs", Name: queryID, Mode: "pivot",
		Rows: "service", Cols: "service",
	})
	require.True(t, result.IsError, "same-key pivot should error")
	assert.Contains(t, resultText(result), "must differ")
}

// TestLogsQuery_PivotUnknownKey asserts that when the caller picks a
// label key that no stream carries, the pivot renders an empty-matrix
// message (not a panic and not a truncated table). This is the "bad
// choice of keys" UX path.
func TestLogsQuery_PivotUnknownKey(t *testing.T) {
	queryID := "q-pivot-unknown"
	h := setupLogTestHandler(t, queryID)
	result := h.handleLogsQuery(context.Background(), queryArgs{
		Graph: "logs", Name: queryID, Mode: "pivot",
		Rows: "nonexistent_key", Cols: "pod",
	})
	require.False(t, result.IsError, "unknown key should render empty, not error: %s", resultText(result))
	text := resultText(result)
	// Every stream was skipped because none carry "nonexistent_key".
	assert.Contains(t, text, "No streams carry both pivot keys",
		"empty matrix message should surface when keys don't match any stream")
}

// TestPivot_CountsMatchStreamFixture asserts the Pivot engine method
// produces the expected counts for a hand-rolled fixture. This is the
// unit-level complement to the handler tests — it verifies the
// aggregation math without going through the pipeline.
func TestPivot_CountsMatchStreamFixture(t *testing.T) {
	// Two templates: one ERROR, one INFO.
	errTmpl := &logwire.LogTemplate{ID: "t-err", Pattern: "boom", Severity: logwire.SeverityError}
	infoTmpl := &logwire.LogTemplate{ID: "t-info", Pattern: "ok", Severity: logwire.SeverityInfo}

	// Two streams: one covers (node=A, reason=Kill), the other (node=B, reason=Restart).
	streamA := &logwire.LogStream{ID: "s-a",
		Labels: map[string]string{"node": "A", "reason": "Kill"}}
	streamB := &logwire.LogStream{ID: "s-b",
		Labels: map[string]string{"node": "B", "reason": "Restart"}}
	// One stream missing a key — should be skipped, not crash.
	streamC := &logwire.LogStream{ID: "s-c",
		Labels: map[string]string{"node": "C"}}

	// Chunks: A gets 3 errors + 2 infos; B gets 1 error.
	chunks := []*logwire.LogChunk{
		{ID: "c-a-err", StreamID: "s-a", TemplateID: "t-err", EntryCount: 3},
		{ID: "c-a-info", StreamID: "s-a", TemplateID: "t-info", EntryCount: 2},
		{ID: "c-b-err", StreamID: "s-b", TemplateID: "t-err", EntryCount: 1},
	}

	engine := logs.NewQueryEngine(
		[]*logwire.LogStream{streamA, streamB, streamC},
		chunks,
		[]*logwire.LogTemplate{errTmpl, infoTmpl},
	)

	r := engine.Pivot("node", "reason")
	require.NotNil(t, r)
	assert.Equal(t, 2, r.StreamsCovered, "two streams carry both keys")
	assert.Equal(t, 1, r.StreamsSkipped, "one stream is missing reason")

	// Cell (A, Kill) = 5 total, 3 error.
	aKill := r.Cells["A"]["Kill"]
	require.NotNil(t, aKill, "cell A,Kill should exist")
	assert.Equal(t, 5, aKill.TotalCount)
	assert.Equal(t, 3, aKill.ErrorCount)

	// Cell (B, Restart) = 1 total, 1 error.
	bRestart := r.Cells["B"]["Restart"]
	require.NotNil(t, bRestart, "cell B,Restart should exist")
	assert.Equal(t, 1, bRestart.TotalCount)
	assert.Equal(t, 1, bRestart.ErrorCount)

	// Grand total.
	assert.Equal(t, 6, r.Grand.TotalCount)
	assert.Equal(t, 4, r.Grand.ErrorCount)

	// Rows ordered by total desc: A (5) before B (1).
	require.Len(t, r.Rows, 2)
	assert.Equal(t, "A", r.Rows[0], "A should come first by total")
	assert.Equal(t, "B", r.Rows[1])
}

// TestPivot_EmptyEngine asserts a no-data engine produces an empty but
// well-formed result rather than a nil panic.
func TestPivot_EmptyEngine(t *testing.T) {
	engine := logs.NewQueryEngine(nil, nil, nil)
	r := engine.Pivot("service", "pod")
	require.NotNil(t, r)
	assert.Equal(t, 0, r.StreamsCovered)
	assert.Equal(t, 0, r.StreamsSkipped)
	assert.Empty(t, r.Rows)
	assert.Empty(t, r.Cols)
	assert.Equal(t, 0, r.Grand.TotalCount)
}

// TestFormatPivot_EmptyCellRenderedAsDot confirms the formatter shows
// "·" for empty cells so sparse matrices stay scannable. Uses a 2×2
// fixture where one diagonal is empty.
func TestFormatPivot_EmptyCellRenderedAsDot(t *testing.T) {
	// Single stream — (A, Kill) only. (A, Restart), (B, Kill), (B, Restart)
	// will all be empty.
	errTmpl := &logwire.LogTemplate{ID: "t-err", Pattern: "x", Severity: logwire.SeverityError}
	stream := &logwire.LogStream{ID: "s",
		Labels: map[string]string{"node": "A", "reason": "Kill"}}
	chunk := &logwire.LogChunk{ID: "c", StreamID: "s", TemplateID: "t-err", EntryCount: 1}
	engine := logs.NewQueryEngine([]*logwire.LogStream{stream}, []*logwire.LogChunk{chunk},
		[]*logwire.LogTemplate{errTmpl})

	h := &Handler{}
	// Stash the engine so handleLogsPivot can find it — we don't need the
	// persisted graph for formatter tests.
	logs.RegisterEngine("q-fmt", engine)
	t.Cleanup(func() { logs.UnregisterEngine("q-fmt") })

	// Can't use handleLogsQuery here because it requires a persisted graph
	// for cold-rebuild; call handleLogsPivot directly.
	result := handleLogsPivot("q-fmt", engine, "node", "reason")
	require.False(t, result.IsError)
	txt := resultText(result)
	// Single populated cell + single row + single col, so no "·" is
	// actually rendered in this 1x1 matrix. Extend: introduce a second
	// stream with a different row so the empty (B, Kill) cell is visible.
	assert.Contains(t, txt, "1 (1 err)", "populated cell should render total+err")
	// Add another stream and recompute to validate the "·" path.
	streamB := &logwire.LogStream{ID: "s2",
		Labels: map[string]string{"node": "B", "reason": "Restart"}}
	chunkB := &logwire.LogChunk{ID: "cB", StreamID: "s2", TemplateID: "t-err", EntryCount: 2}
	engine2 := logs.NewQueryEngine(
		[]*logwire.LogStream{stream, streamB},
		[]*logwire.LogChunk{chunk, chunkB},
		[]*logwire.LogTemplate{errTmpl},
	)
	result2 := handleLogsPivot("q-fmt2", engine2, "node", "reason")
	require.False(t, result2.IsError)
	txt2 := resultText(result2)
	// The (A, Restart) and (B, Kill) cells are empty and should render as "·".
	assert.Contains(t, txt2, "·",
		"sparse cells should render as '·', got: %s", txt2)
	_ = h
}
