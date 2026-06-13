// SPDX-License-Identifier: Apache-2.0

package thought

// topic_create.go holds the CREATE stage of the lever's topic lifecycle: the
// eligibility gate + the batched topic-summary document persistence.

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// topicMinSize is the HIGH-conservative minimum member count for a topic to earn
// a summary document. Plan-time const; lowered incrementally during shake-out.
const topicMinSize = 5

// createReport tallies the topic-document creates for the lever's loud report.
type createReport struct {
	Created   int               // new topic docs created this pass
	summaries map[string]string // cluster_id → summary text, for report detail
}

// topicEligibleForCreate reports whether a topic earns a NEW summary document:
// it is large enough (Size >= topicMinSize), its primary cluster_id is already
// persisted-stable (a brand-new-this-pass label is not yet stable), it has a
// usable identity anchor (MedoidID + Centroid), and no existing doc is already
// anchored to its medoid (idempotency per topic identity).
func topicEligibleForCreate(tp Topic, existingByMedoid map[string]bool, stablePrimary map[string]bool) bool {
	if tp.Size < topicMinSize {
		return false
	}
	if tp.MedoidID == "" || len(tp.Centroid) == 0 {
		return false
	}
	if existingByMedoid[tp.MedoidID] {
		return false // already has a doc → no duplicate
	}
	if !stablePrimary[tp.PrimaryClusterID] {
		return false // brand-new label this pass → not yet stable
	}
	return true
}

// createTopicDocs persists one embeddable `document` node per eligible topic that
// lacks a doc — the CREATE stage of the lever's topic lifecycle, after
// reconciliation. It hands ALL eligible topics to the TopicSummarizer, which runs
// them through a bounded-parallel chunked pass (envelope-safe per-call batches),
// then writes ONE create_batch carrying every new doc node plus its relates-to
// edge to the topic medoid. Each doc's Description holds the one-line summary so
// the document auto-embeds (NodeDocument ShouldEmbed:ifDescriptionPresent), and
// metadata carries the durable identity anchor (medoid_id) + cluster_id +
// topic_centroid + member_clusters.
//
// Partial-failure isolation: a summarize error degrades only the failed batch's
// topics (they get no summary and are skipped); the surviving topics are still
// persisted, and the summarize error is returned alongside the populated report so
// the caller records a stage error without discarding the survivors.
//
// A nil summarizer (degraded client) creates nothing and returns a zero report.
// Topics whose medoid already has a doc are skipped (idempotent per identity), so
// re-running the pass never spawns a duplicate.
func createTopicDocs(
	ctx context.Context,
	gc Caller,
	topics []Topic,
	existingByMedoid map[string]bool,
	stablePrimary map[string]bool,
	summarizer TopicSummarizer,
) (createReport, error) {
	rep := createReport{summaries: map[string]string{}}
	if summarizer == nil {
		return rep, nil // degraded: no summaries, no creates
	}

	// Filter to the eligible topics lacking a doc.
	var eligible []Topic
	for _, tp := range topics {
		if topicEligibleForCreate(tp, existingByMedoid, stablePrimary) {
			eligible = append(eligible, tp)
		}
	}
	if len(eligible) == 0 {
		return rep, nil // nothing eligible → zero LLM calls, zero creates
	}

	// Hand all eligible topics to the summarizer's bounded-parallel chunked pass.
	// A summarize error is CAPTURED (not early-returned): the survivors that came
	// back are still persisted below, and summarizeErr is returned at the end so the
	// caller records a stage error without discarding them.
	inputs := make([]TopicInput, len(eligible))
	for i, tp := range eligible {
		inputs[i] = TopicInput{ClusterID: tp.PrimaryClusterID, Content: tp.SummaryContent}
	}
	summaries, summarizeErr := summarizer.SummarizeTopics(ctx, inputs)
	if summarizeErr != nil {
		summarizeErr = fmt.Errorf("thought: createTopicDocs: summarize: %w", summarizeErr)
	}
	summaryByCID := make(map[string]string, len(summaries))
	for _, s := range summaries {
		summaryByCID[s.ClusterID] = s.Summary
	}

	// Build ONE create_batch: a document node + a relates-to edge to the medoid
	// per eligible topic that got a non-empty summary.
	//
	// The topic prompt also emits per-item keywords, but they are intentionally NOT
	// persisted here: the create_batch carrier has no keywords field (keywords only
	// thread through the UPDATE path), so landing them on the create would require
	// widening the node body across the wire and server decode — an out-of-scope
	// server/proto change. BM25 still indexes the doc's name + description + summary,
	// so topic docs stay searchable without the keyword list.
	var nodes []map[string]any
	var edges []map[string]any
	for _, tp := range eligible {
		summary := summaryByCID[tp.PrimaryClusterID]
		if summary == "" {
			continue // no summary produced → skip (no empty-Description doc)
		}
		slot := len(nodes)
		nodes = append(nodes, map[string]any{
			"type":        string(kgtypes.NodeDocument),
			"name":        summary,
			"description": summary, // ShouldEmbed:ifDescriptionPresent → auto-embeds
			// document is a summary-REQUIRED type (create-time validation rejects an
			// empty summary field); clamp to the 500-char wire cap defensively.
			"summary": clampSummary(summary),
			"metadata": map[string]string{
				metaClusterID:      tp.PrimaryClusterID,
				metaMedoidID:       tp.MedoidID,
				metaTopicCentroid:  encodeCentroid(tp.Centroid),
				metaMemberClusters: encodeMemberClusters(tp.MemberClusters),
			},
		})
		edges = append(edges, map[string]any{
			"from_idx": slot,
			"to_id":    tp.MedoidID,
			"type":     string(kgtypes.EdgeRelatesTo),
		})
		rep.summaries[tp.PrimaryClusterID] = summary
	}
	if len(nodes) == 0 {
		return rep, summarizeErr // survivors empty (e.g. whole batch failed) → report the summarize error
	}

	args, err := json.Marshal(map[string]any{
		"operation": "create_batch",
		"nodes":     nodes,
		"edges":     edges,
	})
	if err != nil {
		return rep, fmt.Errorf("thought: createTopicDocs: marshal create_batch: %w", err)
	}
	if _, err := executeViaEngine(ctx, gc, "mutate", args); err != nil {
		return rep, fmt.Errorf("thought: createTopicDocs: create write: %w", err)
	}
	rep.Created = len(nodes)
	return rep, summarizeErr // survivors landed; surface any partial-batch summarize failure as a stage error
}

// clampSummary truncates a topic summary to the 500-char node-summary wire cap
// (create-time validation rejects longer), cutting on a rune boundary so a
// multi-byte character is never split.
func clampSummary(s string) string {
	const maxLen = 500
	if len(s) <= maxLen {
		return s
	}
	runes := []rune(s)
	if len(runes) > maxLen {
		runes = runes[:maxLen]
	}
	out := string(runes)
	for len(out) > maxLen { // multi-byte runes: shrink until under the byte cap
		runes = runes[:len(runes)-1]
		out = string(runes)
	}
	return out
}
