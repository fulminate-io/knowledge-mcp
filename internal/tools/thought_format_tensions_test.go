// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	clientthought "github.com/fulminate-io/knowledge-mcp/internal/thought"
)

// sampleTensionReports builds a constructed tensions slice with one collapsed
// representative pair, used by the render + json + no-recompute tests. The handler
// is now a pure cache serve (TensionsProvider.GetTensions), so the reports are fed
// directly through the provider seam — no gc-driven recompute fixture is needed.
func sampleTensionReports() []clientthought.TensionReport {
	return []clientthought.TensionReport{{
		NodeA:        &knowledgev1.Node{Id: "t-a", SymbolName: "optimistic stance"},
		NodeB:        &knowledgev1.Node{Id: "t-b", SymbolName: "pessimistic stance"},
		PropertiesA:  clientthought.ThoughtProperties{Valence: 0.8, ChargeCount: 3},
		PropertiesB:  clientthought.ThoughtProperties{Valence: -0.7, ChargeCount: 2},
		ValenceDelta: 1.5,
		EdgeType:     "contradicts",
		PairCount:    2,
	}}
}

// TestHandleReflectTensions (FAILS-WHEN-ABSENT): the text render leads with the
// tensions header and names both thoughts + the provenance edge + the collapse
// line; the json format returns the served reports.
func TestHandleReflectTensions(t *testing.T) {
	deps := interceptTestDeps{tensions: sampleTensionReports(), tensionsComputed: true}

	text := resultText(handleReflectTensions(context.Background(), deps, queryReflectArgs{}))
	assert.True(t, strings.HasPrefix(text, "# Unresolved Tensions"), "render leads with the tensions header; got: %s", text)
	assert.Contains(t, text, "optimistic stance", "names thought A")
	assert.Contains(t, text, "pessimistic stance", "names thought B")
	assert.Contains(t, text, "contradicts", "renders the provenance edge type")
	assert.Contains(t, text, "collapses 2 similar pairs", "renders the collapsed-pair count")

	jsonText := resultText(handleReflectTensions(context.Background(), deps, queryReflectArgs{Format: "json"}))
	assert.Contains(t, jsonText, "optimistic stance", "json carries thought A")
	assert.Contains(t, jsonText, "contradicts", "json carries the edge type")
}

// manyTensionReports builds n distinct representative pairs so a render cap has
// something to cut. Each pair carries a unique symbol name so a truncated render
// is distinguishable from a reordered one.
func manyTensionReports(n int) []clientthought.TensionReport {
	reports := make([]clientthought.TensionReport, 0, n)
	for i := range n {
		reports = append(reports, clientthought.TensionReport{
			NodeA:        &knowledgev1.Node{Id: fmt.Sprintf("t-a-%d", i), SymbolName: fmt.Sprintf("stance-a-%d", i)},
			NodeB:        &knowledgev1.Node{Id: fmt.Sprintf("t-b-%d", i), SymbolName: fmt.Sprintf("stance-b-%d", i)},
			PropertiesA:  clientthought.ThoughtProperties{Valence: 0.8, ChargeCount: 3},
			PropertiesB:  clientthought.ThoughtProperties{Valence: -0.7, ChargeCount: 2},
			ValenceDelta: 1.5,
			EdgeType:     "contradicts",
			PairCount:    1,
		})
	}
	return reports
}

// countRenderedTensionRows counts the numbered rows of a tensions text render.
// The renderer writes one "N. **A** ... vs **B**" line per representative, so the
// count of lines matching that shape IS the number of rendered tensions.
func countRenderedTensionRows(text string) int {
	rowRe := regexp.MustCompile(`^\d+\. \*\*`)
	rows := 0
	for line := range strings.SplitSeq(text, "\n") {
		if rowRe.MatchString(line) {
			rows++
		}
	}
	return rows
}

// TestReflectTensions_LimitCapsRenderedRows (FAILS-WHEN-ABSENT): query(mode:tensions,
// limit:N) is SERVED rather than refused, renders exactly N rows out of a larger
// cached report, and the totals header's "showing top" value agrees with N. The
// both-directions leg is the last subtest: an ABSENT limit renders the whole cached
// report unchanged, without which every leg above is satisfiable by an arm that
// always returns an empty list.
func TestReflectTensions_LimitCapsRenderedRows(t *testing.T) {
	const cached = 7
	const want = 3
	deps := interceptTestDeps{tensions: manyTensionReports(cached), tensionsComputed: true}

	t.Run("text render caps at limit", func(t *testing.T) {
		text := resultText(handleReflectTensions(context.Background(), deps, queryReflectArgs{Limit: want}))
		assert.NotContains(t, text, "limit is not applied by this path", "the limit is served, not refused")
		assert.Equal(t, want, countRenderedTensionRows(text), "renders exactly the requested number of rows")
		assert.Contains(t, text, fmt.Sprintf("showing top %d", want),
			"the totals header's showing-top value equals the requested limit; got: %s", text)
		assert.Contains(t, text, "stance-a-0", "keeps the top-ranked representative")
		assert.NotContains(t, text, "stance-a-6", "drops the representatives past the cap")
	})

	t.Run("json render caps at limit", func(t *testing.T) {
		// Sliced BEFORE the format branch, so a json caller asking for N gets N.
		jsonText := resultText(handleReflectTensions(context.Background(), deps, queryReflectArgs{Limit: want, Format: "json"}))
		assert.Contains(t, jsonText, "stance-a-0", "json carries the top-ranked representative")
		assert.NotContains(t, jsonText, "stance-a-6", "json is capped too")
	})

	t.Run("absent limit renders the full cached report", func(t *testing.T) {
		text := resultText(handleReflectTensions(context.Background(), deps, queryReflectArgs{}))
		assert.Equal(t, cached, countRenderedTensionRows(text), "an absent limit renders every cached representative")
		assert.Contains(t, text, fmt.Sprintf("showing top %d", cached), "the header reports the full cached count")
		assert.Contains(t, text, "stance-a-6", "the last representative survives when no limit is supplied")
	})

	t.Run("limit above the cached count renders everything", func(t *testing.T) {
		text := resultText(handleReflectTensions(context.Background(), deps, queryReflectArgs{Limit: cached + 5}))
		assert.Equal(t, cached, countRenderedTensionRows(text), "an over-large limit does not pad or truncate")
	})
}

// TestHandleReflectTensions_ColdStart (FAILS-WHEN-ABSENT): a not-yet-computed cache
// (computed=false) renders the cold message, NOT an empty report.
func TestHandleReflectTensions_ColdStart(t *testing.T) {
	deps := interceptTestDeps{tensionsComputed: false} // cold sentinel.

	text := resultText(handleReflectTensions(context.Background(), deps, queryReflectArgs{}))
	assert.Equal(t, tensionsColdMessage, text, "cold cache renders the not-yet-computed message")
}

// TestHandleReflectTensions_NilProvider (FAILS-WHEN-ABSENT): a nil TensionsProvider
// (reflection loop not running) renders the loop-not-running message rather than
// panicking or recomputing.
func TestHandleReflectTensions_NilProvider(t *testing.T) {
	deps := interceptTestDeps{tensionsProviderNil: true}

	text := resultText(handleReflectTensions(context.Background(), deps, queryReflectArgs{}))
	assert.Contains(t, text, "reflection loop is not running", "nil provider renders the loop-not-running message")
}

// TestHandleReflectTensions_NoRecompute (FAILS-WHEN-ABSENT, ticket-mandated): tensions
// is the pure zero-Execute cache serve — the handler issues ZERO Execute calls on the
// gc. Any non-zero count means a recompute (ReflectTensions) leaked onto the call path.
// The provider returns a computed report so the handler takes the full render path.
func TestHandleReflectTensions_NoRecompute(t *testing.T) {
	gc := &countingExecCaller{}
	deps := interceptTestDeps{gc: gc, tensions: sampleTensionReports(), tensionsComputed: true}

	// Text and json both exercise the served path.
	_ = handleReflectTensions(context.Background(), deps, queryReflectArgs{})
	_ = handleReflectTensions(context.Background(), deps, queryReflectArgs{Format: "json"})

	assert.Equal(t, int64(0), gc.calls.Load(),
		"tensions cache-serve handler must issue ZERO Execute calls — a non-zero count means a recompute leaked onto the call path")
}
