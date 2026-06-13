// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"testing"

	"github.com/stretchr/testify/assert"

	clientthought "github.com/fulminate-io/knowledge-mcp/internal/thought"
)

// TestSimilarityReport_RenderArtifactLink (FAILS-WHEN-ABSENT): a report carrying
// ArtifactLinkPerArtifact + ArtifactLinkEdgesTotal renders the
// '## Shared-Artifact Clique (artifact-link)' section with one loud per-artifact line
// (artifact name + short id + thought count + edges this pass) per entry plus the total.
// These per-artifact lines are also the measurement readout (qualifying-artifact count =
// line count, biggest = max thought count, total = the total line).
func TestSimilarityReport_RenderArtifactLink(t *testing.T) {
	report := clientthought.SimilarityReport{
		ArtifactLinkPerArtifact: []clientthought.ArtifactLinkStat{
			{ArtifactID: "dec-alpha-hash-1234", ArtifactName: "Pick θ against true residue", ThoughtCount: 5, EdgesWritten: 10},
			{ArtifactID: "find-beta-hash-5678", ArtifactName: "Census of singleton thoughts", ThoughtCount: 2, EdgesWritten: 0},
		},
		ArtifactLinkEdgesTotal: 10,
	}
	body := renderSimilarityReport(report)

	assert.Contains(t, body, "## Shared-Artifact Clique", "the artifact-link section header is present")
	assert.Contains(t, body, "Pick θ against true residue", "the first artifact's name is loud")
	assert.Contains(t, body, "5 thoughts, 10 edges this pass", "the first artifact's count + edges this pass are loud")
	assert.Contains(t, body, "Census of singleton thoughts", "the second artifact's name is loud")
	assert.Contains(t, body, "2 thoughts, 0 edges this pass",
		"an artifact whose edges already exist still renders its loud line (0 edges this pass)")
	assert.Contains(t, body, "Total artifact-link edges written: 10", "the total is rendered")
}

// TestSimilarityReport_RenderArtifactLinkSkipped (FAILS-WHEN-ABSENT): a non-empty
// ArtifactLinkSkippedReason renders a loud SKIPPED line and suppresses the per-artifact
// lines; an empty report still renders the header with a zero total.
func TestSimilarityReport_RenderArtifactLinkSkipped(t *testing.T) {
	report := clientthought.SimilarityReport{
		ArtifactLinkSkippedReason: "artifact-link resolution failed — no clique edges written",
		ArtifactLinkPerArtifact: []clientthought.ArtifactLinkStat{
			{ArtifactID: "x", ArtifactName: "should-not-render", ThoughtCount: 2, EdgesWritten: 1},
		},
	}
	body := renderSimilarityReport(report)

	assert.Contains(t, body, "## Shared-Artifact Clique", "the header still renders")
	assert.Contains(t, body, "SKIPPED: artifact-link resolution failed", "the loud SKIPPED line renders")
	assert.NotContains(t, body, "should-not-render", "per-artifact lines are suppressed when the phase was skipped")

	// An empty/zero report renders the header with a zero total, no per-artifact lines.
	empty := clientthought.SimilarityReport{TopicCount: 1}
	bodyEmpty := renderSimilarityReport(empty)
	assert.Contains(t, bodyEmpty, "Total artifact-link edges written: 0",
		"a pass with no grouping artifacts still renders the section with a zero total")
	assert.Contains(t, bodyEmpty, "## Shared-Artifact Clique", "the section header always renders")
}

// TestSimilarityReport_RenderChainOrder (FAILS-WHEN-ABSENT): the artifact-link section
// renders AFTER tree-link (render-chain order preserved).
func TestSimilarityReport_RenderChainOrder(t *testing.T) {
	report := clientthought.SimilarityReport{
		TreeLinkEdgesTotal:     1,
		ArtifactLinkEdgesTotal: 1,
	}
	body := renderSimilarityReport(report)

	treeIdx := indexOf(body, "## Tree-Link")
	artIdx := indexOf(body, "## Shared-Artifact Clique")

	assert.Positive(t, treeIdx, "tree-link section present")
	assert.Positive(t, artIdx, "artifact-link section present")
	assert.Less(t, treeIdx, artIdx, "artifact-link renders AFTER tree-link")
}
