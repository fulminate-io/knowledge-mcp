// SPDX-License-Identifier: Apache-2.0

package engine

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// practiceFixtureResult builds a SearchResult with the importance/category
// metadata the practice renderers surface.
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

// perHitBlock extracts the "### N. …" per-hit blocks from a rendered body,
// dropping the leading header line(s) so the single-language and fan-out
// renderers can be compared on per-hit lines alone.
func perHitBlocks(text string) string {
	idx := strings.Index(text, "### ")
	if idx < 0 {
		return ""
	}
	return text[idx:]
}

// TestRenderPracticeFanOut_ParityWithSingle proves the acceptance shape: the
// shared writePracticeHit helper makes RenderPracticeResults and
// RenderPracticeFanOut emit identical per-hit lines for the same SearchResult
// (the fan-out renderer additionally tags each hit with its source graph), and
// RenderPracticeFanOut emits the "Searched N practice graphs" header.
func TestRenderPracticeFanOut_ParityWithSingle(t *testing.T) {
	r := practiceFixtureResult("p:1", "WorkerPool", "bound goroutines with a semaphore", 0.91)

	single := RenderPracticeResults("go", "pool", []SearchResult{r}).Content[0].Text
	fan := RenderPracticeFanOut("pool", []string{"go"}, []PracticeFanOutHit{{Graph: "go", Result: r}}).Content[0].Text

	// Fan-out header names the graph count + the graphs searched.
	assert.Contains(t, fan, "Searched 1 practice graphs (go)")

	// The single-language renderer emits the un-tagged per-hit block; the fan-out
	// renderer emits the same block with the source-graph tag appended to the header.
	singleBlock := perHitBlocks(single)
	fanBlock := perHitBlocks(fan)
	assert.Equal(t, "### 1. WorkerPool [high] (concurrency)\n0.91 — bound goroutines with a semaphore\nID: p:1 | Status: active\n\n", singleBlock)
	assert.Equal(t, "### 1. WorkerPool [high] (concurrency) — go\n0.91 — bound goroutines with a semaphore\nID: p:1 | Status: active\n\n", fanBlock)

	// The per-hit lines are byte-identical once the graph tag is stripped — the
	// only difference the fan-out introduces is the " — <graph>" attribution.
	assert.Equal(t, singleBlock, strings.Replace(fanBlock, " — go\n", "\n", 1))
}

// TestRenderPracticeFanOut_MultiGraphAttribution asserts each merged hit is
// tagged with its own source graph and the header counts every searched graph.
func TestRenderPracticeFanOut_MultiGraphAttribution(t *testing.T) {
	goHit := PracticeFanOutHit{Graph: "go", Result: practiceFixtureResult("p:go", "GoPattern", "go content", 0.80)}
	pyHit := PracticeFanOutHit{Graph: "python", Result: practiceFixtureResult("p:py", "PyPattern", "py content", 0.70)}

	text := RenderPracticeFanOut("pattern", []string{"go", "python"}, []PracticeFanOutHit{goHit, pyHit}).Content[0].Text

	assert.Contains(t, text, "Searched 2 practice graphs (go, python)")
	assert.Contains(t, text, "### 1. GoPattern [high] (concurrency) — go")
	assert.Contains(t, text, "### 2. PyPattern [high] (concurrency) — python")
}
