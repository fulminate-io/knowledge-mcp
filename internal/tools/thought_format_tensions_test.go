// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
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
		ThoughtA:     &knowledgev1.Node{Id: "t-a", SymbolName: "optimistic stance"},
		ThoughtB:     &knowledgev1.Node{Id: "t-b", SymbolName: "pessimistic stance"},
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
