// SPDX-License-Identifier: Apache-2.0

package thought

// topic_drift.go holds the DRIFT stage of the lever's topic lifecycle: detecting
// topics whose live centroid has diverged from the centroid stored at their last
// summary and re-summarizing them.

import (
	"context"
	"encoding/json"
	"fmt"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// topicDriftBound is the HIGH-conservative drift threshold: a topic re-summarizes
// only when its live centroid diverges from the centroid stored at its last summary
// by more than this fraction of bits. Plan-time const; lowered incrementally during
// shake-out.
const topicDriftBound = 0.20

// driftReport tallies the drift-driven re-summaries for the lever's loud report.
type driftReport struct {
	Checked     int               // topic docs drift-checked
	Resummaried int               // docs that drifted past the bound and were refreshed
	summaries   map[string]string // cluster_id → new summary, for report detail
}

// driftTopicDocs re-summarizes topic docs whose live centroid has diverged from the
// centroid stored at their last summary — the DRIFT stage of the lever's topic
// lifecycle, after create. Drift is measured against the stored anchor:
//
//	drift = 1 - BitSimilarity(liveCentroid, storedCentroid)
//
// where liveCentroid is the topic's current (possibly cascade-union) centroid and
// storedCentroid = decodeCentroid of the doc's topic_centroid metadata (the
// centroid as of its last summary, written at create and at each drift-refresh).
// Anchoring to the stored centroid — NOT to a re-embedding of the summary text —
// gives the hard guarantee: unchanged membership → identical live and stored
// centroids → drift exactly 0 → no LLM call. (A summary-text embedding sits in a
// different bit-space than a member centroid, so comparing the two re-summarized
// unchanged topics every pass — the oscillation this metric replaces.)
//
// A topic that drifts past topicDriftBound is re-summarized (all drifted topics run
// through the summarizer's bounded-parallel chunked pass) and its doc updated (name +
// description = the new summary, plus refreshed topic_centroid / member_clusters
// metadata, which re-anchors the stored centroid to the new live one). A within-bound
// topic triggers NO LLM call and NO update. A doc with no stored topic_centroid is
// skipped (no anchor to drift-check against). A malformed stored centroid decodes to
// nil → BitSimilarity 0 → drift 1.0 → one self-healing refresh rewrites a valid anchor.
//
// Partial-failure isolation: a summarize error degrades only the failed batch's
// drifted topics (they get no new summary and are skipped); the surviving topics are
// still updated, and the summarize error is returned alongside the populated report
// so the caller records a stage error without discarding the survivors.
//
// A nil summarizer (degraded client) does no drift work and returns a zero report.
func driftTopicDocs(
	ctx context.Context,
	gc Caller,
	existingDocs []*knowledgev1.Node,
	topicByMedoid map[string]Topic,
	summarizer TopicSummarizer,
) (driftReport, error) {
	rep := driftReport{summaries: map[string]string{}}
	if summarizer == nil {
		return rep, nil // degraded: drift can't run without the summarizer
	}

	var toResummary []driftedTopic

	for _, doc := range existingDocs {
		medoid := kgtypes.Value(doc, metaMedoidID)
		tp, ok := topicByMedoid[medoid]
		if !ok || len(tp.Centroid) == 0 {
			continue // no live topic for this doc → nothing to drift-check
		}
		storedHex := kgtypes.Value(doc, metaTopicCentroid)
		if storedHex == "" {
			continue // no stored anchor → cannot drift-check
		}
		rep.Checked++

		stored := decodeCentroid(storedHex)
		drift := 1 - BitSimilarity(tp.Centroid, stored)
		if drift > topicDriftBound {
			toResummary = append(toResummary, driftedTopic{doc: doc, topic: tp})
		}
	}

	if len(toResummary) == 0 {
		return rep, nil // every topic within bound → no LLM, no update
	}

	// Re-summarize only the drifted topics through the summarizer's bounded-parallel
	// chunked pass. A summarize error is CAPTURED (not early-returned): the survivors
	// that came back are still updated below, and summarizeErr is returned at the end
	// so the caller records a stage error without discarding them.
	inputs := make([]TopicInput, len(toResummary))
	for i, d := range toResummary {
		inputs[i] = TopicInput{ClusterID: d.topic.PrimaryClusterID, Content: d.topic.SummaryContent}
	}
	summaries, summarizeErr := summarizer.SummarizeTopics(ctx, inputs)
	if summarizeErr != nil {
		summarizeErr = fmt.Errorf("thought: driftTopicDocs: re-summarize: %w", summarizeErr)
	}
	summaryByCID := make(map[string]string, len(summaries))
	for _, s := range summaries {
		summaryByCID[s.ClusterID] = s.Summary
	}

	// Update each drifted doc with the new summary text + refreshed provenance. A
	// write failure here is a hard error and returns immediately (it is NOT the
	// summarize-failure path); the captured summarizeErr is surfaced only after every
	// survivor is written.
	for _, d := range toResummary {
		newSummary := summaryByCID[d.topic.PrimaryClusterID]
		if newSummary == "" {
			continue
		}
		if err := writeDriftUpdate(ctx, gc, d, newSummary); err != nil {
			return rep, err
		}
		rep.Resummaried++
		rep.summaries[d.topic.PrimaryClusterID] = newSummary
	}

	return rep, summarizeErr // survivors updated; surface any partial-batch summarize failure as a stage error
}

// driftedTopic pairs a topic that exceeded the drift bound with the doc to update.
type driftedTopic struct {
	doc   *knowledgev1.Node
	topic Topic
}

// writeDriftUpdate issues the by-id mutate(update) for one drifted doc: it sets
// name + description to the new summary (so the doc re-embeds) and refreshes the
// provenance metadata (cluster_id / medoid_id / topic_centroid / member_clusters).
func writeDriftUpdate(ctx context.Context, gc Caller, d driftedTopic, newSummary string) error {
	args, err := json.Marshal(map[string]any{
		"operation":   "update",
		"id":          d.doc.Id,
		"name":        newSummary,
		"description": newSummary,
		"metadata": map[string]string{
			metaClusterID:      d.topic.PrimaryClusterID,
			metaMedoidID:       d.topic.MedoidID,
			metaTopicCentroid:  encodeCentroid(d.topic.Centroid),
			metaMemberClusters: encodeMemberClusters(d.topic.MemberClusters),
		},
	})
	if err != nil {
		return fmt.Errorf("thought: driftTopicDocs: marshal update: %w", err)
	}
	if _, err := executeViaEngine(ctx, gc, "mutate", args); err != nil {
		return fmt.Errorf("thought: driftTopicDocs: update write: %w", err)
	}
	return nil
}
