// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"strings"
	"testing"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/collector"
	"github.com/fulminate-io/knowledge-mcp/internal/embed"
	"github.com/fulminate-io/knowledge-mcp/internal/hivemonitor"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	clientthought "github.com/fulminate-io/knowledge-mcp/internal/thought"
)

// reflectFakeCaller serves a tiny deterministic thought corpus + topic docs so the
// reflect handlers can run end-to-end without a real backend. It dispatches by
// query shape: a type=thought browse returns the seeded thoughts (with cluster_id
// metadata), a type=document browse returns the seeded topic docs, every other
// read returns empty.
type reflectFakeCaller struct {
	thoughts []*knowledgev1.Node
	docs     []*knowledgev1.Node
}

func (c *reflectFakeCaller) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	q := req.GetQuery()
	if q == nil {
		return &knowledgev1.ExecuteResponse{}, nil
	}
	switch q.GetSelection().GetNodeType() {
	case string(kgtypes.NodeThought):
		if q.GetOffset() > 0 {
			return &knowledgev1.ExecuteResponse{}, nil
		}
		return &knowledgev1.ExecuteResponse{Nodes: c.thoughts}, nil
	case string(kgtypes.NodeDocument):
		if q.GetOffset() > 0 {
			return &knowledgev1.ExecuteResponse{}, nil
		}
		return &knowledgev1.ExecuteResponse{Nodes: c.docs}, nil
	default:
		return &knowledgev1.ExecuteResponse{}, nil
	}
}

// reflectTestDeps is a minimal ClientDeps whose GraphCaller returns the fake.
// clusters/profile back the ClusterProvider seam personality/summary now read from
// (post cache-serve repoint). reflectGranularityDeps builds them from the same
// reflectCorpus() the gc serves, so they are byte-identical to what the pre-cache
// fetchClusterContext path produced — keeping the granularity assertions unchanged.
type reflectTestDeps struct {
	gc       GraphCaller
	clusters []clientthought.ThoughtCluster
	profile  *clientthought.PersonalityProfile
}

// reflectGranularityDeps wires reflectTestDeps with a ClusterProvider whose clusters
// + personality profile are derived from the supplied gc via the SAME
// DetectPersistedClusters + ComputePersonalityScalars path fetchClusterContext used
// before the cache-serve repoint, so the granularity tests see byte-identical
// clusters/profile through the provider seam.
func reflectGranularityDeps(gc *reflectFakeCaller) reflectTestDeps {
	ctx := context.Background()
	clusters, err := clientthought.DetectPersistedClusters(ctx, gc)
	if err != nil {
		panic("reflectGranularityDeps: DetectPersistedClusters: " + err.Error())
	}
	var profile *clientthought.PersonalityProfile
	if len(clusters) > 0 {
		p, err := clientthought.ComputePersonalityScalars(ctx, gc, clusters, clientthought.BuildEvidenceAdj(ctx, gc, clusters))
		if err != nil {
			panic("reflectGranularityDeps: ComputePersonalityScalars: " + err.Error())
		}
		profile = &p
	}
	return reflectTestDeps{gc: gc, clusters: clusters, profile: profile}
}

func (d reflectTestDeps) LocalLiveness() LocalLiveness                 { return nil }
func (d reflectTestDeps) Sink() collector.Sink                         { return nil }
func (d reflectTestDeps) RootDir() string                              { return "" }
func (d reflectTestDeps) UsageAnalyzer() UsageAnalyzerAPI              { return nil }
func (d reflectTestDeps) WorkerRuntime() WorkerRuntimeAPI              { return nil }
func (d reflectTestDeps) WorkerReady() bool                            { return true }
func (d reflectTestDeps) PropReady() bool                              { return true }
func (d reflectTestDeps) PipelineReady() bool                          { return true }
func (d reflectTestDeps) ClaimRegistry() *hivemonitor.Registry         { return nil }
func (d reflectTestDeps) BanSet() *hivemonitor.BanSet                  { return nil }
func (d reflectTestDeps) WorkerCRUD() WorkerCRUDAPI                    { return nil }
func (d reflectTestDeps) GraphTypeCRUD() GraphTypeCRUDAPI              { return nil }
func (d reflectTestDeps) Embedder() embed.BinaryEmbedder               { return nil }
func (d reflectTestDeps) BackendResolver() BackendResolver             { return nil }
func (d reflectTestDeps) GraphCaller() GraphCaller                     { return d.gc }
func (d reflectTestDeps) LocalGraphCaller() GraphCaller                { return d.gc }
func (d reflectTestDeps) SegmentManager() SegmentSearcher              { return nil }
func (d reflectTestDeps) SegmentVectorResolver() SegmentVectorResolver { return nil }
func (d reflectTestDeps) SegmentShipper() SegmentShipper               { return nil }
func (d reflectTestDeps) SegmentPruner() SegmentPruner                 { return nil }
func (d reflectTestDeps) SegmentCoverage() SegmentCoverageReader       { return nil }
func (d reflectTestDeps) PipelineScanner() PipelineScanner             { return nil }
func (d reflectTestDeps) ReflectionForcer() ReflectionForcer           { return nil }
func (d reflectTestDeps) SimilarityForcer() SimilarityForcer           { return nil }

func (d reflectTestDeps) BlindSpotProvider() BlindSpotProvider { return nil }

func (d reflectTestDeps) ClusterProvider() ClusterProvider {
	return fakeClusterProvider{clusters: d.clusters, profile: d.profile, computed: true}
}
func (d reflectTestDeps) TensionsProvider() TensionsProvider { return nil }

func reflectThought(id, clusterID string) *knowledgev1.Node {
	n := &knowledgev1.Node{Id: id, Type: string(kgtypes.NodeThought), SymbolName: "member-" + id}
	kgtypes.SetValue(n, "cluster_id", clusterID)
	return n
}

func reflectTopicDoc(id, clusterID, members, summary string) *knowledgev1.Node {
	n := &knowledgev1.Node{Id: id, Type: string(kgtypes.NodeDocument), Description: summary}
	kgtypes.SetValue(n, "cluster_id", clusterID)
	kgtypes.SetValue(n, "medoid_id", "medoid-"+clusterID)
	kgtypes.SetValue(n, "member_clusters", members)
	return n
}

// corpus: c1 + c4 share Topic One (collapse), c2 = Topic Two, c3 topicless.
func reflectCorpus() *reflectFakeCaller {
	return &reflectFakeCaller{
		thoughts: []*knowledgev1.Node{
			reflectThought("t1", "c1"), reflectThought("t1b", "c1"),
			reflectThought("t2", "c2"),
			reflectThought("t3", "c3"),
			reflectThought("t4", "c4"),
		},
		docs: []*knowledgev1.Node{
			reflectTopicDoc("doc1", "c1", "c1,c4", "Topic One"),
			reflectTopicDoc("doc2", "c2", "c2", "Topic Two"),
		},
	}
}

// TestReflectGranularity_ClusterDefaultByteCompatible asserts that
// granularity:"cluster" and an absent/empty granularity render byte-identical
// output for both summary and personality (the cluster path is the untouched
// default code).
func TestReflectGranularity_ClusterDefaultByteCompatible(t *testing.T) {
	deps := reflectGranularityDeps(reflectCorpus())
	ctx := context.Background()

	sumEmpty := resultText(handleReflectSummary(ctx, deps, queryReflectArgs{Granularity: ""}))
	sumCluster := resultText(handleReflectSummary(ctx, deps, queryReflectArgs{Granularity: "cluster"}))
	if sumEmpty != sumCluster {
		t.Fatalf("summary: empty vs cluster granularity differ:\n--- empty ---\n%s\n--- cluster ---\n%s", sumEmpty, sumCluster)
	}

	persEmpty := resultText(handleReflectPersonality(ctx, deps, queryReflectArgs{Granularity: ""}))
	persCluster := resultText(handleReflectPersonality(ctx, deps, queryReflectArgs{Granularity: "cluster"}))
	if persEmpty != persCluster {
		t.Fatalf("personality: empty vs cluster granularity differ:\n--- empty ---\n%s\n--- cluster ---\n%s", persEmpty, persCluster)
	}
}

// TestReflectGranularity_TopicRollup asserts the topic view rolls clusters up by
// topic membership: SUMMARY collapses c1+c4 into one "Topic One" row (size summed)
// and a topicless cluster appears as its own row; PERSONALITY relabels cluster-pair
// rows to topic summaries with the scalar unchanged.
func TestReflectGranularity_TopicRollup(t *testing.T) {
	deps := reflectGranularityDeps(reflectCorpus())
	ctx := context.Background()

	// SUMMARY topic view.
	sumTopic := resultText(handleReflectSummary(ctx, deps, queryReflectArgs{Granularity: "topic"}))
	sumCluster := resultText(handleReflectSummary(ctx, deps, queryReflectArgs{Granularity: "cluster"}))
	if sumTopic == sumCluster {
		t.Fatalf("topic-granularity summary must differ from cluster default:\n%s", sumTopic)
	}
	// Topic One spans c1(2 thoughts) + c4(1 thought) → one row of 3 thoughts.
	if !containsLine(sumTopic, "Topic One", "3 thoughts") {
		t.Fatalf("summary topic view missing collapsed 'Topic One (3 thoughts)' row:\n%s", sumTopic)
	}
	// A topicless cluster (c3) appears as its own row under its member label.
	if !containsSub(sumTopic, "member-t3") && !containsSub(sumTopic, "c3") {
		t.Fatalf("summary topic view dropped the topicless c3 row:\n%s", sumTopic)
	}

	// PERSONALITY topic view: the c1↔c2 pair relabels to (Topic One, Topic Two).
	persTopic := resultText(handleReflectPersonality(ctx, deps, queryReflectArgs{Granularity: "topic"}))
	if !containsSub(persTopic, "Topic One") || !containsSub(persTopic, "Topic Two") {
		t.Fatalf("personality topic view missing topic-summary labels:\n%s", persTopic)
	}
}

// containsSub reports whether sub appears in s.
func containsSub(s, sub string) bool {
	return strings.Contains(s, sub)
}

// containsLine reports whether some line of s contains BOTH substrings.
func containsLine(s, a, b string) bool {
	for line := range strings.SplitSeq(s, "\n") {
		if strings.Contains(line, a) && strings.Contains(line, b) {
			return true
		}
	}
	return false
}
