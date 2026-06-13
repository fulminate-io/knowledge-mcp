// SPDX-License-Identifier: Apache-2.0

package thought

// topic_labels.go holds the read-side of the topic layer: the persisted-topic-doc
// reads the reflect render surfaces use to (a) override a cluster's display label
// with its topic summary and (b) build the granularity-rollup view that collapses
// clusters sharing a topic.

import (
	"context"
	"sort"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// topicLabelsByClusterID browses the persisted topic `document` nodes and returns
// a map of cluster_id → topic-summary text (the doc's Description). It is the
// shared read both the display-name override and the granularity rollup use, so
// they never re-query. Returns nil on a nil client or a corpus with no topic docs.
func topicLabelsByClusterID(ctx context.Context, gc Caller) map[string]string {
	if gc == nil {
		return nil
	}
	docs, err := drainThoughtBrowse(ctx, gc, string(kgtypes.NodeDocument), browsePageSize)
	if err != nil || len(docs) == 0 {
		return nil
	}
	labels := make(map[string]string)
	for _, doc := range docs {
		cid := kgtypes.Value(doc, metaClusterID)
		if cid == "" {
			continue // not a topic doc (no cluster_id anchor)
		}
		if summary := doc.GetDescription(); summary != "" {
			labels[cid] = summary
		}
	}
	if len(labels) == 0 {
		return nil
	}
	return labels
}

// ApplyTopicLabels overrides cluster display labels with persisted topic-summary
// text wherever a topic doc exists for a cluster_id — used by the reflect render
// surfaces so a summarized group shows its topic name instead of the highest-
// magnitude member's SymbolName. A cluster with no topic doc keeps its prior
// member-text label (no regression for unsummarized groups). Mutates clusters and
// profile.ClusterLabels in place.
func ApplyTopicLabels(ctx context.Context, gc Caller, clusters []ThoughtCluster, profile *PersonalityProfile) {
	labels := topicLabelsByClusterID(ctx, gc)
	if len(labels) == 0 {
		return // no topic docs → leave every label as the member-text fallback
	}
	for i := range clusters {
		if label, ok := labels[clusters[i].ID]; ok {
			clusters[i].Label = label
		}
	}
	if profile != nil {
		for cid := range profile.ClusterLabels {
			if label, ok := labels[cid]; ok {
				profile.ClusterLabels[cid] = label
			}
		}
	}
}

// TopicGrouping is the granularity-rollup view of the persisted topic docs:
// TopicOf maps each member cluster_id (and the topic's primary label) to its
// topic key (the primary cluster_id), and Labels maps a topic key to its summary
// text. A cluster absent from TopicOf is topicless and rolls up as itself.
type TopicGrouping struct {
	TopicOf map[string]string // cluster_id → topic key (primary cluster_id)
	Labels  map[string]string // topic key → topic summary text
}

// TopicGroupingByClusterID browses the persisted topic docs and builds the
// granularity-rollup view: every member cluster of a topic maps to the topic's
// primary cluster_id, and that key carries the topic summary text. Returns a
// zero-value grouping (empty maps) when there are no topic docs.
func TopicGroupingByClusterID(ctx context.Context, gc Caller) TopicGrouping {
	g := TopicGrouping{TopicOf: map[string]string{}, Labels: map[string]string{}}
	if gc == nil {
		return g
	}
	docs, err := drainThoughtBrowse(ctx, gc, string(kgtypes.NodeDocument), browsePageSize)
	if err != nil {
		return g
	}
	for _, doc := range docs {
		primary := kgtypes.Value(doc, metaClusterID)
		if primary == "" {
			continue
		}
		summary := doc.GetDescription()
		if summary != "" {
			g.Labels[primary] = summary
		}
		g.TopicOf[primary] = primary
		for _, label := range decodeMemberClusters(kgtypes.Value(doc, metaMemberClusters)) {
			g.TopicOf[label] = primary
		}
	}
	return g
}

// topicKeyFor returns the topic key for a cluster_id, defaulting to the cluster
// itself when it belongs to no topic (topicless → its own row).
func (g TopicGrouping) topicKeyFor(clusterID string) string {
	if k, ok := g.TopicOf[clusterID]; ok {
		return k
	}
	return clusterID
}

// labelFor returns the display label for a topic key: the topic summary when one
// exists, else the supplied fallback (the member-text cluster label).
func (g TopicGrouping) labelFor(topicKey, fallback string) string {
	if l, ok := g.Labels[topicKey]; ok && l != "" {
		return l
	}
	return fallback
}

// RollupSummaryTopics collapses clusters that share a topic into one rolled-up
// ThoughtCluster: Size = Σ member-cluster sizes, AvgValence/AvgMagnitude =
// size-weighted mean over the member clusters, Label = the topic summary. A
// topicless cluster appears as its own row (its own label). The result is sorted
// by rolled-up Size descending so the caller can slice the top rows as today.
func RollupSummaryTopics(clusters []ThoughtCluster, g TopicGrouping) []ThoughtCluster {
	type acc struct {
		size      int
		weightedV float64
		weightedM float64
		fallback  string
	}
	byTopic := map[string]*acc{}
	var order []string
	for _, c := range clusters {
		key := g.topicKeyFor(c.ID)
		a, ok := byTopic[key]
		if !ok {
			a = &acc{fallback: c.Label}
			byTopic[key] = a
			order = append(order, key)
		}
		a.size += c.Size
		a.weightedV += c.AvgValence * float64(c.Size)
		a.weightedM += c.AvgMagnitude * float64(c.Size)
	}
	out := make([]ThoughtCluster, 0, len(order))
	for _, key := range order {
		a := byTopic[key]
		var avgV, avgM float64
		if a.size > 0 {
			avgV = a.weightedV / float64(a.size)
			avgM = a.weightedM / float64(a.size)
		}
		out = append(out, ThoughtCluster{
			ID:           key,
			Label:        g.labelFor(key, a.fallback),
			Size:         a.size,
			AvgValence:   avgV,
			AvgMagnitude: avgM,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		// Size desc with an ID (topic key) tie-break for deterministic rollup order.
		if out[i].Size != out[j].Size {
			return out[i].Size > out[j].Size
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// RollupPersonalityTopics groups the personality cluster-pair rows by
// (topicOf(A), topicOf(B)) FOR DISPLAY: the pair labels are relabeled to the
// topic summaries and rows mapping to the same topic-pair collapse into one
// (keeping the first row's scalar, in the report's existing extreme-first order).
// The Scalar values are the UNCHANGED cluster-pair quantities — a deliberate v1
// choice, since averaging a calibrated cross-cluster trust scalar across a topic's
// member clusters has no defined meaning. This is presentation-only rollup.
func RollupPersonalityTopics(report PersonalityReport, g TopicGrouping) PersonalityReport {
	report.TopStubborn = rollupPairs(report.TopStubborn, g)
	report.TopGullible = rollupPairs(report.TopGullible, g)
	return report
}

// rollupPairs relabels each cluster-pair to its topic-pair and dedups rows that
// map to the same (topicA, topicB), preserving input order (the report already
// sorted by the extreme scalar, so the first occurrence is the representative).
func rollupPairs(pairs []ClusterPairScalar, g TopicGrouping) []ClusterPairScalar {
	seen := map[string]bool{}
	var out []ClusterPairScalar
	for _, p := range pairs {
		topicA := g.topicKeyFor(p.ClusterA)
		topicB := g.topicKeyFor(p.ClusterB)
		key := topicA + "\x00" + topicB
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, ClusterPairScalar{
			ClusterA: topicA,
			ClusterB: topicB,
			LabelA:   g.labelFor(topicA, p.LabelA),
			LabelB:   g.labelFor(topicB, p.LabelB),
			Scalar:   p.Scalar, // UNCHANGED — calibrated cluster-pair quantity
		})
	}
	return out
}
