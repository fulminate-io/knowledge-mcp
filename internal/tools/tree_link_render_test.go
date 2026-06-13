// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"testing"

	"github.com/stretchr/testify/assert"

	clientthought "github.com/fulminate-io/knowledge-mcp/internal/thought"
)

// TestSimilarityReport_RenderTreeLink (FAILS-WHEN-ABSENT): a report carrying
// TreeLinkPerTree + TreeLinkEdgesTotal renders the '## Tree-Link' section with one loud
// per-tree line (root name + thought count + edges this pass) per entry plus the total.
// The section text is part of renderSimilarityReport's output, so it rides the persisted
// event body verbatim (similarity_render_test.go:18).
func TestSimilarityReport_RenderTreeLink(t *testing.T) {
	report := clientthought.SimilarityReport{
		TreeLinkPerTree: []clientthought.TreeLinkTreeStat{
			{RootID: "proj-alpha-hash-1234", RootName: "Reflect/retro feedback loop", ThoughtCount: 7, EdgesWritten: 21},
			{RootID: "ticket-beta-hash-5678", RootName: "Tree-link write mode", ThoughtCount: 3, EdgesWritten: 0},
		},
		TreeLinkEdgesTotal: 21,
	}
	body := renderSimilarityReport(report)

	assert.Contains(t, body, "## Tree-Link", "the tree-link section header is present")
	// One loud per-tree line per entry: root name + thought count + edges this pass.
	assert.Contains(t, body, "Reflect/retro feedback loop", "the first tree's root name is loud")
	assert.Contains(t, body, "7 thoughts, 21 edges this pass", "the first tree's count + edges this pass are loud")
	assert.Contains(t, body, "Tree-link write mode", "the second tree's root name is loud")
	assert.Contains(t, body, "3 thoughts, 0 edges this pass",
		"a tree whose edges already exist still renders its loud line (0 edges this pass)")
	assert.Contains(t, body, "Total tree-link edges written: 21", "the total is rendered")
}

// TestSimilarityReport_RenderTreeLinkSkipped (FAILS-WHEN-ABSENT): a non-empty
// TreeLinkSkippedReason renders a loud SKIPPED line and suppresses the per-tree lines.
func TestSimilarityReport_RenderTreeLinkSkipped(t *testing.T) {
	report := clientthought.SimilarityReport{
		TreeLinkSkippedReason: "tree-link root resolution failed — no clique edges written",
		// PerTree is set but must NOT render when the phase was skipped.
		TreeLinkPerTree: []clientthought.TreeLinkTreeStat{
			{RootID: "x", RootName: "should-not-render", ThoughtCount: 2, EdgesWritten: 1},
		},
	}
	body := renderSimilarityReport(report)

	assert.Contains(t, body, "## Tree-Link", "the header still renders")
	assert.Contains(t, body, "SKIPPED: tree-link root resolution failed", "the loud SKIPPED line renders")
	assert.NotContains(t, body, "should-not-render", "per-tree lines are suppressed when the phase was skipped")

	// An empty/zero report renders the header with a zero total, no per-tree lines.
	empty := clientthought.SimilarityReport{TopicCount: 1}
	bodyEmpty := renderSimilarityReport(empty)
	assert.Contains(t, bodyEmpty, "Total tree-link edges written: 0",
		"a pass with no grouping trees still renders the section with a zero total")
	assert.Contains(t, bodyEmpty, "## Tree-Link", "the section header always renders")
}
