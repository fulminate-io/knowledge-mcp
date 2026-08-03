// SPDX-License-Identifier: Apache-2.0

// similarity_report_render_test.go — renderSimilarityReport's OUTPUT contract,
// split out of similarity_dispatch_test.go (which owns the dispatch/trigger
// lifecycle and the shared fakes). These tests build a SimilarityReport directly
// and assert on the rendered text, so they need none of that file's fixtures.

package tools

import (
	"testing"

	"github.com/stretchr/testify/assert"

	clientthought "github.com/fulminate-io/knowledge-mcp/internal/thought"
)

// TestSimilarityReport_Render: the rendered result lists links (pairs+scores),
// merge cascade chains (A+B→AB, AB+C→ABC + scores), summaries gen/refreshed, and
// reconciliation re-key/merge/tombstone counts.
func TestSimilarityReport_Render(t *testing.T) {
	report := clientthought.SimilarityReport{
		LinkThreshold:       0.90,
		MergeThreshold:      0.97,
		TopicCount:          4,
		SummaryVectorBacked: 3,
		MergeChains: []clientthought.MergeChain{
			{From: []string{"A", "B"}, To: "AB", Sim: 0.98},
			{From: []string{"AB", "C"}, To: "ABC", Sim: 0.975},
		},
		LinksCreated: []clientthought.LinkCandidate{
			{MedoidA: "mX", MedoidB: "mY", Score: 0.93},
		},
		AlreadyLinked:      2,
		SummariesCreated:   3,
		SummariesRefreshed: 1,
		Rekeyed:            5,
		Merged:             1,
		Tombstoned:         2,
		TombstonedDocs: []clientthought.TombstonedDoc{
			{ID: "doc-loser", Name: "Topic about auth"},
			{ID: "doc-orphan", Name: "Topic about caching"},
		},
		StageErrors: []string{"topic create failed: boom"},
		SimBuckets: []clientthought.SimBucket{
			{Lo: 0.90, Hi: 0.95, Count: 41},
		},
		NearMisses: []clientthought.LinkCandidate{
			{MedoidA: "mNear1", MedoidB: "mNear2", Score: 0.897},
		},
	}
	body := renderSimilarityReport(report)

	// Summary-vector coverage line — N/M topics summary-vector-backed.
	assert.Contains(t, body, "Summary-vector-backed: 3/4 topics")
	// Links + scores.
	assert.Contains(t, body, "mX")
	assert.Contains(t, body, "mY")
	assert.Contains(t, body, "0.93")
	// Merge cascade chains.
	assert.Contains(t, body, "A+B → AB")
	assert.Contains(t, body, "AB+C → ABC")
	// Summaries + reconciliation counts.
	assert.Contains(t, body, "3 generated")
	assert.Contains(t, body, "1 refreshed")
	assert.Contains(t, body, "5 re-keyed")
	assert.Contains(t, body, "2 tombstoned")
	// Stage errors render LOUDLY above the counts — a swallowed per-stage failure
	// (the live-found bug: topic create failed server-side, the report said
	// "0 generated" with no failure line) must be visible in the tool result.
	assert.Contains(t, body, "STAGE ERRORS (1)")
	assert.Contains(t, body, "topic create failed: boom")
	// Threshold-tuning survey: histogram buckets + near-miss pairs below link.
	assert.Contains(t, body, "Similarity Distribution")
	assert.Contains(t, body, "[0.90, 0.95): 41 pairs")
	assert.Contains(t, body, "Near Misses")
	assert.Contains(t, body, "mNear1 ↔ mNear2 (sim 0.897)")
	// Tombstoned docs listed by id+name (the soft delete must be auditable, never
	// a bare count).
	assert.Contains(t, body, "soft delete")
	assert.Contains(t, body, "doc-loser")
	assert.Contains(t, body, "Topic about auth")
	assert.Contains(t, body, "doc-orphan")
	assert.Contains(t, body, "Topic about caching")
}

// TestSimilarityReport_RenderDensify (FAILS-WHEN-ABSENT) asserts the densify section
// of the rendered lever result: per-topic edges + before/after components, a total,
// the loud DENSIFY BUDGET HIT line naming the budget + remediation, and the
// structural-estimate caveat footnote.
func TestSimilarityReport_RenderDensify(t *testing.T) {
	report := clientthought.SimilarityReport{
		DensifyPerTopic: []clientthought.TopicDensifyStat{
			{TopicKey: "topicA", EdgesWritten: 4, BeforeComponents: 3, AfterComponents: 1},
		},
		DensifyEdgesTotal: 4,
		DensifyBudgetHit:  true,
		DensifyBudget:     4,
		DensifyStarved:    2,
	}
	body := renderSimilarityReport(report)

	assert.Contains(t, body, "Densification")
	assert.Contains(t, body, "topicA")
	assert.Contains(t, body, "4 densify edges")
	assert.Contains(t, body, "components 3→1")
	assert.Contains(t, body, "Total densify edges written: 4")
	assert.Contains(t, body, "DENSIFY BUDGET HIT")
	assert.Contains(t, body, "2 topics truncated")
	assert.Contains(t, body, "densify_edge_budget", "the loud line names the remediation knob")
	assert.Contains(t, body, "NOT Leiden communities", "the structural-estimate caveat footnote is present")
}

// TestSimilarityReport_RenderDensifySkipped (FAILS-WHEN-ABSENT) asserts a nil-scanner
// run renders the loud DensifySkippedReason rather than an empty/silent section.
func TestSimilarityReport_RenderDensifySkipped(t *testing.T) {
	report := clientthought.SimilarityReport{
		DensifySkippedReason: "no member-vector scanner wired (no drain) — densification SKIPPED (no edges written)",
	}
	body := renderSimilarityReport(report)
	assert.Contains(t, body, "Densification")
	assert.Contains(t, body, "SKIPPED")
	assert.Contains(t, body, "no member-vector scanner wired")
}
