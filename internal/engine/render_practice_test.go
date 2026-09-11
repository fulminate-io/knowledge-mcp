// SPDX-License-Identifier: Apache-2.0

package engine

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// practiceFixtureResult builds a SearchResult with the importance/category
// metadata the practice renderer surfaces.
func practiceFixtureResult(id, name, content string, score float64) SearchResult {
	return SearchResult{
		Node: &knowledgev1.Node{
			Id:         id,
			SymbolName: name,
			Status:     "active",
			Content:    content,
			Metadata: map[string]string{
				"importance": "high",
				"category":   "concurrency",
			},
		},
		Score: score,
	}
}

// perHitBlocks extracts the "### N. …" per-hit blocks from a rendered body,
// dropping the leading header line(s) so the per-hit shape is asserted on its own.
func perHitBlocks(text string) string {
	idx := strings.Index(text, "### ")
	if idx < 0 {
		return ""
	}
	return text[idx:]
}

// TestRenderPracticeResults_HeaderAndPerHitShape pins the rendered shape: the
// family header that names no graph instance, and the per-hit block with its
// importance and category, score-and-content line, and id-and-status line, with
// no per-graph attribution appended (the cross-graph tag went with the fan-out
// renderer, which had no caller once the practice graphs were combined).
func TestRenderPracticeResults_HeaderAndPerHitShape(t *testing.T) {
	r := practiceFixtureResult("p:1", "WorkerPool", "bound goroutines with a semaphore", 0.91)

	text := RenderPracticeResults("pool", []SearchResult{r}, "").Content[0].Text

	assert.True(t, strings.HasPrefix(text, "## Best Practices — 1 result for \"pool\"\n\n"), text)
	assert.Equal(t, "### 1. WorkerPool [high] (concurrency)\n0.91 — bound goroutines with a semaphore\nID: p:1 | Status: active\n\n", perHitBlocks(text))
}

// TestRenderPracticeResults_HeaderPluralizesTheCount is the conditional half of
// the header, and it takes TWO rows because one cannot prove a condition.
//
// THE SINGULAR ROW ALONE IS SATISFIED BY A SUFFIX THAT IS ALWAYS EMPTY, which is
// the regression the plural fix could introduce: the header read "%d results"
// unconditionally, so a single hit rendered "1 results", and a helper hard-wired
// the other way renders "2 result". The zero row is here because an empty result
// set is a real render — the arm returns it with a qualifying notice rather than
// an error — and English wants the plural for it.
func TestRenderPracticeResults_HeaderPluralizesTheCount(t *testing.T) {
	one := practiceFixtureResult("p:1", "WorkerPool", "bound goroutines with a semaphore", 0.91)
	two := practiceFixtureResult("p:2", "Confinement", "one goroutine owns the value", 0.80)

	for _, tc := range []struct {
		name    string
		results []SearchResult
		want    string
	}{
		{name: "zero", results: nil, want: "## Best Practices — 0 results for \"pool\"\n\n"},
		{name: "one", results: []SearchResult{one}, want: "## Best Practices — 1 result for \"pool\"\n\n"},
		{name: "two", results: []SearchResult{one, two}, want: "## Best Practices — 2 results for \"pool\"\n\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			text := RenderPracticeResults("pool", tc.results, "").Content[0].Text
			assert.True(t, strings.HasPrefix(text, tc.want),
				"want prefix %q, got %q", tc.want, text)
		})
	}
}

// TestRenderPracticeResults_SearchModeFooter pins the always-on arm disclosure:
// present, as renderText's footer, when the caller reports an arm; absent when it
// has none to report.
func TestRenderPracticeResults_SearchModeFooter(t *testing.T) {
	r := practiceFixtureResult("p:1", "WorkerPool", "bound goroutines with a semaphore", 0.91)

	withMode := RenderPracticeResults("pool", []SearchResult{r}, "vector+text").Content[0].Text
	assert.True(t, strings.HasSuffix(withMode, "\n_search mode: vector+text_\n"), withMode)

	noMode := RenderPracticeResults("pool", []SearchResult{r}, "").Content[0].Text
	assert.NotContains(t, noMode, "_search mode:")
}
