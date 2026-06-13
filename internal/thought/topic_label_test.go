// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"context"
	"testing"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// docBrowseCaller serves a type=document browse: the first page returns the
// scripted topic docs, subsequent pages are empty (terminating the drain).
type docBrowseCaller struct {
	docs  []*knowledgev1.Node
	calls int
}

func (c *docBrowseCaller) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	q := req.GetQuery()
	if q == nil {
		return &knowledgev1.ExecuteResponse{}, nil
	}
	c.calls++
	// First page serves all docs; any subsequent (offset>0) page is empty so the
	// offset-paging drain terminates.
	if q.GetOffset() > 0 {
		return &knowledgev1.ExecuteResponse{}, nil
	}
	return &knowledgev1.ExecuteResponse{Nodes: c.docs}, nil
}

// topicDocFor builds a topic document node carrying a cluster_id + a summary in
// its Description.
func topicDocFor(id, clusterID, summary string) *knowledgev1.Node {
	n := &knowledgev1.Node{Id: id, Type: string(kgtypes.NodeDocument), Description: summary}
	kgtypes.SetValue(n, metaClusterID, clusterID)
	return n
}

// TestClusterLabel_TopicOverride: ApplyTopicLabels sets a cluster's Label to its
// topic-summary text when a topic doc exists, and leaves a topicless cluster's
// member-text label untouched.
func TestClusterLabel_TopicOverride(t *testing.T) {
	gc := &docBrowseCaller{docs: []*knowledgev1.Node{
		topicDocFor("doc-c1", "c1", "Distributed consensus topic"),
	}}
	clusters := []ThoughtCluster{
		{ID: "c1", Label: "member-symbol-name-A"},
		{ID: "c2", Label: "member-symbol-name-B"}, // no topic doc
	}

	ApplyTopicLabels(context.Background(), gc, clusters, nil)

	if clusters[0].Label != "Distributed consensus topic" {
		t.Fatalf("c1 Label = %q, want the topic summary", clusters[0].Label)
	}
	if clusters[1].Label != "member-symbol-name-B" {
		t.Fatalf("c2 Label = %q, want the member fallback (no topic doc)", clusters[1].Label)
	}
}

// TestReflectSummary_TopicLabel: a cluster with a topic doc renders its topic
// summary in ReflectSummary TopClusters.
func TestReflectSummary_TopicLabel(t *testing.T) {
	gc := &docBrowseCaller{docs: []*knowledgev1.Node{
		topicDocFor("doc-c1", "c1", "Topic One"),
	}}
	clusters := []ThoughtCluster{{ID: "c1", Label: "member-A", Size: 9}}

	ApplyTopicLabels(context.Background(), gc, clusters, nil)
	summary := ReflectSummary(context.Background(), gc, clusters)

	if len(summary.TopClusters) != 1 {
		t.Fatalf("TopClusters = %d, want 1", len(summary.TopClusters))
	}
	if summary.TopClusters[0].Label != "Topic One" {
		t.Fatalf("TopClusters[0].Label = %q, want Topic One", summary.TopClusters[0].Label)
	}
}

// TestReflectPersonality_TopicLabel: a cluster with a topic doc renders its topic
// summary as the ClusterLabels label feeding ReflectPersonality; a topicless
// cluster keeps its member-text label.
func TestReflectPersonality_TopicLabel(t *testing.T) {
	gc := &docBrowseCaller{docs: []*knowledgev1.Node{
		topicDocFor("doc-c1", "c1", "Topic One"),
	}}
	clusters := []ThoughtCluster{{ID: "c1", Label: "member-A"}, {ID: "c2", Label: "member-B"}}
	profile := &PersonalityProfile{
		ClusterLabels: map[string]string{"c1": "member-A", "c2": "member-B"},
		Scalars: map[string]map[string]float64{
			"c1": {"c2": 0.3},
		},
	}

	ApplyTopicLabels(context.Background(), gc, clusters, profile)

	if profile.ClusterLabels["c1"] != "Topic One" {
		t.Fatalf("ClusterLabels[c1] = %q, want Topic One", profile.ClusterLabels["c1"])
	}
	if profile.ClusterLabels["c2"] != "member-B" {
		t.Fatalf("ClusterLabels[c2] = %q, want member fallback", profile.ClusterLabels["c2"])
	}

	report := ReflectPersonality(clusters, profile, "")
	// The c1→c2 pair should carry the topic label on side A.
	found := false
	for _, p := range report.TopStubborn {
		if p.ClusterA == "c1" && p.LabelA == "Topic One" {
			found = true
		}
	}
	if !found {
		t.Fatalf("ReflectPersonality did not surface the topic label on the c1 pair: %+v", report.TopStubborn)
	}
}
