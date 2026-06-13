// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"context"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// createCaller records CREATE NodeBodies + Edges so the test can assert exactly
// one document create with the right metadata and a relates-to edge to the medoid.
type createCaller struct {
	createCalls int
	nodeBodies  []*knowledgev1.NodeBody
	edges       []*knowledgev1.BatchEdgeSpec
}

func (c *createCaller) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	m := req.GetMutation()
	if m != nil && m.GetKind() == knowledgev1.MutationPlan_MUTATION_KIND_CREATE {
		c.createCalls++
		c.nodeBodies = append(c.nodeBodies, m.GetNodeBodies()...)
		c.edges = append(c.edges, m.GetEdges()...)
	}
	return &knowledgev1.ExecuteResponse{}, nil
}

// stubTopicSummarizer returns "topic: <cluster_id>" for every input and records
// how many times it was called.
type stubTopicSummarizer struct{ calls int }

func (s *stubTopicSummarizer) SummarizeTopics(_ context.Context, inputs []TopicInput) ([]TopicSummary, error) {
	s.calls++
	out := make([]TopicSummary, len(inputs))
	for i, in := range inputs {
		out[i] = TopicSummary{ClusterID: in.ClusterID, Summary: "topic: " + in.ClusterID}
	}
	return out, nil
}

func eligibleTopic() Topic {
	return Topic{
		PrimaryClusterID: "cidA",
		MemberClusters:   []string{"cidA"},
		Centroid:         bitVec(0, 1, 2),
		MedoidID:         "medoidA",
		Size:             topicMinSize, // exactly eligible
		SummaryContent:   "alpha beta gamma",
	}
}

// TestTopicDocCreate_EligibilityAndMetadata: an eligible topic produces exactly
// one document create carrying cluster_id + medoid_id + topic_centroid +
// member_clusters and a relates-to edge to the medoid; a too-small topic and a
// brand-new (non-stable) topic each produce none.
func TestTopicDocCreate_EligibilityAndMetadata(t *testing.T) {
	tp := eligibleTopic()
	stable := map[string]bool{"cidA": true}
	rec := &createCaller{}
	sum := &stubTopicSummarizer{}

	rep, err := createTopicDocs(context.Background(), rec, []Topic{tp}, nil, stable, sum)
	if err != nil {
		t.Fatalf("create error: %v", err)
	}
	if rep.Created != 1 {
		t.Fatalf("Created = %d, want 1", rep.Created)
	}
	if len(rec.nodeBodies) != 1 {
		t.Fatalf("node bodies = %d, want 1", len(rec.nodeBodies))
	}
	body := rec.nodeBodies[0]
	if body.GetType() != "document" {
		t.Fatalf("node type = %q, want document", body.GetType())
	}
	if body.GetDescription() == "" {
		t.Fatalf("node Description empty — must hold the summary so it auto-embeds")
	}
	// document is a summary-REQUIRED type: an empty summary field is rejected by
	// create-time validation, so every doc this batch carries must set it. (The
	// live-found bug: name+description were set, summary wasn't, and every topic
	// create ever attempted failed server-side.)
	if body.GetSummary() == "" {
		t.Fatalf("node Summary empty — create-time validation rejects a document without a summary field")
	}
	md := body.GetMetadata()
	if md[metaClusterID] != "cidA" {
		t.Fatalf("metadata cluster_id = %q, want cidA", md[metaClusterID])
	}
	if md[metaMedoidID] != "medoidA" {
		t.Fatalf("metadata medoid_id = %q, want medoidA", md[metaMedoidID])
	}
	if md[metaTopicCentroid] == "" {
		t.Fatalf("metadata topic_centroid empty — must carry the hex centroid")
	}
	if md[metaMemberClusters] != "cidA" {
		t.Fatalf("metadata member_clusters = %q, want cidA", md[metaMemberClusters])
	}
	// relates-to edge to the medoid.
	if len(rec.edges) != 1 {
		t.Fatalf("edges = %d, want 1 (relates-to medoid)", len(rec.edges))
	}
	if rec.edges[0].GetType() != "relates-to" {
		t.Fatalf("edge type = %q, want relates-to", rec.edges[0].GetType())
	}
	if rec.edges[0].GetToId() != "medoidA" {
		t.Fatalf("edge to_id = %q, want medoidA", rec.edges[0].GetToId())
	}

	// Too-small topic → no create.
	small := eligibleTopic()
	small.Size = topicMinSize - 1
	recSmall := &createCaller{}
	repSmall, _ := createTopicDocs(context.Background(), recSmall, []Topic{small}, nil, stable, &stubTopicSummarizer{})
	if repSmall.Created != 0 || recSmall.createCalls != 0 {
		t.Fatalf("too-small topic created %d docs, want 0", repSmall.Created)
	}

	// Brand-new (non-stable) primary label → no create.
	recNew := &createCaller{}
	repNew, _ := createTopicDocs(context.Background(), recNew, []Topic{eligibleTopic()}, nil, map[string]bool{}, &stubTopicSummarizer{})
	if repNew.Created != 0 {
		t.Fatalf("brand-new topic created %d docs, want 0 (not yet stable)", repNew.Created)
	}
}

// TestTopicDocCreate_IdempotentPerMedoid: re-running over a topic whose medoid
// already has a doc creates NO duplicate.
func TestTopicDocCreate_IdempotentPerMedoid(t *testing.T) {
	tp := eligibleTopic()
	stable := map[string]bool{"cidA": true}
	existingByMedoid := map[string]bool{"medoidA": true} // a doc already anchored here

	rec := &createCaller{}
	sum := &stubTopicSummarizer{}
	rep, err := createTopicDocs(context.Background(), rec, []Topic{tp}, existingByMedoid, stable, sum)
	if err != nil {
		t.Fatalf("create error: %v", err)
	}
	if rep.Created != 0 {
		t.Fatalf("Created = %d, want 0 (idempotent — doc already exists for this medoid)", rep.Created)
	}
	if rec.createCalls != 0 {
		t.Fatalf("create calls = %d, want 0", rec.createCalls)
	}
	if sum.calls != 0 {
		t.Fatalf("summarizer calls = %d, want 0 (no eligible topics → no LLM call)", sum.calls)
	}
}

// partialTopicSummarizer returns a summary for every input whose ClusterID is NOT
// in failClusterIDs, omits the failed ones (empty Summary), and returns a non-nil
// error — modeling the partial-batch-failure shape SummarizeTopics produces.
type partialTopicSummarizer struct {
	failClusterIDs map[string]bool
}

func (s *partialTopicSummarizer) SummarizeTopics(_ context.Context, inputs []TopicInput) ([]TopicSummary, error) {
	out := make([]TopicSummary, len(inputs))
	for i, in := range inputs {
		summary := "topic: " + in.ClusterID
		if s.failClusterIDs[in.ClusterID] {
			summary = "" // failed batch → no summary for this cluster
		}
		out[i] = TopicSummary{ClusterID: in.ClusterID, Summary: summary}
	}
	return out, errors.New("one batch failed")
}

// TestTopicDocCreate_SurvivorLandsOnPartialFailure pins the Phase-2 behavior change:
// when the summarizer returns a subset of summaries PLUS an error (a partial-batch
// failure), createTopicDocs must still persist the survivor docs AND return the
// error so the lever records a stage error. A regression to early-return-discard is
// caught here (Created would drop to 0 and the survivor doc would never be written).
func TestTopicDocCreate_SurvivorLandsOnPartialFailure(t *testing.T) {
	survivor := eligibleTopic() // cidA / medoidA
	failed := Topic{
		PrimaryClusterID: "cidB",
		MemberClusters:   []string{"cidB"},
		Centroid:         bitVec(0, 1, 2),
		MedoidID:         "medoidB",
		Size:             topicMinSize,
		SummaryContent:   "delta epsilon zeta",
	}
	stable := map[string]bool{"cidA": true, "cidB": true}
	rec := &createCaller{}
	sum := &partialTopicSummarizer{failClusterIDs: map[string]bool{"cidB": true}}

	rep, err := createTopicDocs(context.Background(), rec, []Topic{survivor, failed}, nil, stable, sum)
	if err == nil {
		t.Fatalf("expected non-nil error so the lever records a stage error")
	}
	if rep.Created != 1 {
		t.Fatalf("Created = %d, want 1 (survivor lands despite the partial failure)", rep.Created)
	}
	if len(rec.nodeBodies) != 1 {
		t.Fatalf("node bodies = %d, want 1 (only the survivor's doc)", len(rec.nodeBodies))
	}
	if md := rec.nodeBodies[0].GetMetadata(); md[metaClusterID] != "cidA" {
		t.Fatalf("survivor doc cluster_id = %q, want cidA", md[metaClusterID])
	}
}

// TestClampSummary pins the 500-byte node-summary wire cap: short strings pass
// through untouched, long ones truncate under the cap, and a multi-byte rune is
// never split (the result stays valid UTF-8 under the byte cap).
func TestClampSummary(t *testing.T) {
	if got := clampSummary("short"); got != "short" {
		t.Fatalf("short summary mutated: %q", got)
	}
	long := strings.Repeat("a", 600)
	if got := clampSummary(long); len(got) != 500 {
		t.Fatalf("long ascii clamp len = %d, want 500", len(got))
	}
	multi := strings.Repeat("é", 400) // 800 bytes, 400 runes
	got := clampSummary(multi)
	if len(got) > 500 {
		t.Fatalf("multi-byte clamp len = %d, want <= 500", len(got))
	}
	if !utf8.ValidString(got) {
		t.Fatalf("clamp split a multi-byte rune — invalid UTF-8")
	}
}
