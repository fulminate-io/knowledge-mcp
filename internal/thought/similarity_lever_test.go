// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// leverFakeCaller serves the reads the similarity lever drives over gc:
//   - type=thought browse → the member thoughts (cluster_id + symbol name);
//   - ids[] hydrate → the same nodes (CreatedAt + symbol name);
//   - type=document browse → existing topic docs (empty here);
//   - RETURN_MODE_EDGES → no existing relates-to edges;
//   - charges_for → none.
//
// It records every MUTATION_KIND_LINK so the degrade test can prove links were
// written even with a nil LLM.
type leverFakeCaller struct {
	thoughts          []*knowledgev1.Node
	links             []linkWrite
	densifyEdges      []*knowledgev1.BatchEdgeSpec // densify create_batch edges (Method topic-densify)
	densifyBatchCalls int                          // create_batch Executes carrying densify edges
}

func (c *leverFakeCaller) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	if m := req.GetMutation(); m != nil {
		if m.GetKind() == knowledgev1.MutationPlan_MUTATION_KIND_LINK {
			from := ""
			if ids := m.GetSelection().GetIds(); len(ids) > 0 {
				from = ids[0]
			}
			c.links = append(c.links, linkWrite{
				from:         from,
				to:           m.GetEdgeSpec().GetToId(),
				method:       m.GetEdgeSpec().GetMethod(),
				relationship: m.GetEdgeSpec().GetRelationship(),
			})
		}
		// Densify edges ride a create_batch (Kind=CREATE) carrying topic-densify edges.
		if m.GetKind() == knowledgev1.MutationPlan_MUTATION_KIND_CREATE {
			var densify []*knowledgev1.BatchEdgeSpec
			for _, e := range m.GetEdges() {
				if e.GetMethod() == "topic-densify" {
					densify = append(densify, e)
				}
			}
			if len(densify) > 0 {
				c.densifyBatchCalls++
				c.densifyEdges = append(c.densifyEdges, densify...)
			}
		}
		return &knowledgev1.ExecuteResponse{}, nil
	}
	q := req.GetQuery()
	if q == nil {
		return &knowledgev1.ExecuteResponse{}, nil
	}
	if q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_EDGES {
		return &knowledgev1.ExecuteResponse{}, nil // no existing edges
	}
	if len(q.GetIds()) > 0 {
		var out []*knowledgev1.Node
		for _, id := range q.GetIds() {
			for _, n := range c.thoughts {
				if n.Id == id {
					out = append(out, n)
				}
			}
		}
		return &knowledgev1.ExecuteResponse{Nodes: out}, nil
	}
	if q.GetOffset() > 0 {
		return &knowledgev1.ExecuteResponse{}, nil
	}
	switch q.GetSelection().GetNodeType() {
	case string(kgtypes.NodeThought):
		return &knowledgev1.ExecuteResponse{Nodes: c.thoughts}, nil
	default:
		// document browse, charge/session counts → empty.
		return &knowledgev1.ExecuteResponse{}, nil
	}
}

// leverThought builds a member thought with cluster_id + symbol name.
func leverThought(id, clusterID string) *knowledgev1.Node {
	n := &knowledgev1.Node{Id: id, Type: string(kgtypes.NodeThought), SymbolName: "sym-" + id, CreatedAt: 1000}
	kgtypes.SetValue(n, "cluster_id", clusterID)
	return n
}

// newLeverLoop builds a PropagationLoop with pre-seeded clusters and an injected
// scanner serving the given member vectors.
func newLeverLoop(gc Caller, scanner PipelineScanner, clusters []ThoughtCluster) *PropagationLoop {
	p := &PropagationLoop{gc: gc, stopCh: make(chan struct{})}
	p.scanner = scanner
	p.lastClusters = clusters
	return p
}

// TestRunSimilarityPass_Defaults: zero-value thresholds fall back to the HIGH
// package-const defaults (link < merge); explicit args override.
func TestRunSimilarityPass_Defaults(t *testing.T) {
	if similarityLinkThresholdDefault >= similarityMergeThresholdDefault {
		t.Fatalf("default link %.2f must be < default merge %.2f", similarityLinkThresholdDefault, similarityMergeThresholdDefault)
	}

	gc := &leverFakeCaller{thoughts: []*knowledgev1.Node{leverThought("t1", "c1")}}
	scanner := &fakeDrainScanner{pages: [][]*knowledgev1.PipelineScanItem{{item("t1", bitVec(0))}}}
	clusters := []ThoughtCluster{{ID: "c1", ThoughtIDs: []string{"t1"}, Size: 1}}
	p := newLeverLoop(gc, scanner, clusters)

	// Zero args → HIGH defaults reflected in the report.
	rep, err := p.RunSimilarityPass(context.Background(), 0, 0, DensifyParams{})
	require.NoError(t, err)
	assert.InDelta(t, similarityLinkThresholdDefault, rep.LinkThreshold, 1e-9)
	assert.InDelta(t, similarityMergeThresholdDefault, rep.MergeThreshold, 1e-9)

	// Explicit args override.
	rep2, err := p.RunSimilarityPass(context.Background(), 0.55, 0.66, DensifyParams{})
	require.NoError(t, err)
	assert.InDelta(t, 0.55, rep2.LinkThreshold, 1e-9)
	assert.InDelta(t, 0.66, rep2.MergeThreshold, 1e-9)
}

// TestDensifyParamsResolve (FAILS-WHEN-ABSENT) asserts the DensifyParams defaulting
// contract the lever relies on: a zero-value field resolves to its densify*Default
// const, and an explicit override is carried through unchanged. Fails if resolve()
// stops defaulting (a zero threshold would then densify nothing) or stops honoring
// overrides (the per-call lever knob would be inert).
func TestDensifyParamsResolve(t *testing.T) {
	// Zero-value → all defaults.
	got := DensifyParams{}.resolve()
	assert.InDelta(t, densifyNodeThresholdDefault, got.Threshold, 1e-9, "zero threshold → default")
	assert.Equal(t, densifyKDefault, got.K, "zero k → default")
	assert.Equal(t, densifyEdgeBudgetDefault, got.EdgeBudget, "zero budget → default")

	// Explicit overrides are used verbatim.
	override := DensifyParams{Threshold: 0.81, K: 5, EdgeBudget: 17}.resolve()
	assert.InDelta(t, 0.81, override.Threshold, 1e-9, "explicit threshold override is used")
	assert.Equal(t, 5, override.K, "explicit k override is used")
	assert.Equal(t, 17, override.EdgeBudget, "explicit budget override is used")

	// The default node threshold is a STRICTER gate than the topic-link threshold.
	assert.Greater(t, densifyNodeThresholdDefault, similarityLinkThresholdDefault,
		"densify is a stricter near-duplicate gate than topic linking")
}

// TestRunSimilarityPassDegrade_LiveScannerNilLLM (FAILS-WHEN-ABSENT): with a live
// scanner but a nil summarizer, the lever computes centroids, runs the cascade, and
// WRITES link edges (centroid-fallback group embedding); zero summaries; the report
// carries the loud degraded note. Fails if a nil summarizer short-circuits the whole
// pass.
func TestRunSimilarityPassDegrade_LiveScannerNilLLM(t *testing.T) {
	// Two clusters whose member vectors are bit-close → above the link threshold
	// but below the merge threshold, so a link edge is written (not a merge).
	v1 := bitVec(0, 1, 2, 3, 4)
	v2 := bitVec(0, 1, 2, 3) // differs from v1 by 1 bit → sim ~0.996 (above link 0.90)
	gc := &leverFakeCaller{thoughts: []*knowledgev1.Node{
		leverThought("t1", "c1"),
		leverThought("t2", "c2"),
	}}
	scanner := &fakeDrainScanner{pages: [][]*knowledgev1.PipelineScanItem{
		{item("t1", v1), item("t2", v2)},
	}}
	clusters := []ThoughtCluster{
		{ID: "c1", ThoughtIDs: []string{"t1"}, Size: 1},
		{ID: "c2", ThoughtIDs: []string{"t2"}, Size: 1},
	}
	p := newLeverLoop(gc, scanner, clusters)
	// nil summarizer (the degraded LLM band) — left unset.

	// Use a link threshold below the pair sim and a merge threshold above it, so the
	// pair links but does not merge.
	rep, err := p.RunSimilarityPass(context.Background(), 0.90, 0.999, DensifyParams{})
	require.NoError(t, err)

	// Links were written despite the nil LLM (centroid-fallback group embedding).
	require.NotEmpty(t, gc.links, "a nil summarizer must NOT short-circuit the link pass — links must still be written")
	assert.Equal(t, "topic-similarity", gc.links[0].method)
	assert.Len(t, rep.LinksCreated, 1)

	// Zero summaries (no summarizer).
	assert.Equal(t, 0, rep.SummariesCreated)
	assert.Equal(t, 0, rep.SummariesRefreshed)

	// The loud degraded note is present.
	assert.True(t, rep.Degraded, "a nil LLM seam must flag the report degraded")
	assert.NotEmpty(t, rep.DegradedNote)
}

// TestRunSimilarityPassDegrade_NilScanner: a nil scanner degrades totally — no
// centroids, no cascade, no links, no summaries — and the report says so loudly.
func TestRunSimilarityPassDegrade_NilScanner(t *testing.T) {
	gc := &leverFakeCaller{thoughts: []*knowledgev1.Node{leverThought("t1", "c1")}}
	clusters := []ThoughtCluster{{ID: "c1", ThoughtIDs: []string{"t1"}, Size: 1}}
	p := newLeverLoop(gc, nil /* nil scanner */, clusters)

	rep, err := p.RunSimilarityPass(context.Background(), 0, 0, DensifyParams{})
	require.NoError(t, err)
	assert.True(t, rep.Degraded)
	assert.Empty(t, gc.links, "a nil scanner must write no links (no centroids → no topic work)")
	assert.Empty(t, rep.LinksCreated)
	// Densification is SKIPPED loudly on a nil scanner (no vector index), no panic.
	assert.NotEmpty(t, rep.DensifySkippedReason, "a nil scanner must set the loud densify skipped reason")
	assert.Empty(t, gc.densifyEdges, "a nil scanner must write zero densify edges")
	assert.Equal(t, 0, rep.DensifyEdgesTotal)
}

// TestRunSimilarityPass_DensifyRunsAndPopulatesReport (FAILS-WHEN-ABSENT) covers the
// densify wiring: with a live scanner (even with a nil LLM), a topic of 2+ near
// members gets within-topic kNN densify edges written via ONE create_batch, and the
// SimilarityReport densify fields are populated. A nil summarizer does NOT
// suppress densify (it is vector-based, needs no LLM).
func TestRunSimilarityPass_DensifyRunsAndPopulatesReport(t *testing.T) {
	// ONE cluster with three near-identical member vectors → one topic, kNN densifies.
	v1 := bitVec(0, 1, 2, 3, 4)
	v2 := bitVec(0, 1, 2, 3) // differs from v1 by 1 bit → sim ~0.996
	v3 := bitVec(0, 1, 2)    // differs from v1 by 2 bits → sim ~0.992
	gc := &leverFakeCaller{thoughts: []*knowledgev1.Node{
		leverThought("t1", "c1"),
		leverThought("t2", "c1"),
		leverThought("t3", "c1"),
	}}
	scanner := &fakeDrainScanner{pages: [][]*knowledgev1.PipelineScanItem{
		{item("t1", v1), item("t2", v2), item("t3", v3)},
	}}
	clusters := []ThoughtCluster{{ID: "c1", ThoughtIDs: []string{"t1", "t2", "t3"}, Size: 3}}
	p := newLeverLoop(gc, scanner, clusters)
	// nil summarizer — densify must STILL run (vector-based).

	// A high merge threshold keeps the single cluster as one topic; a 0.95 densify
	// threshold admits the near-duplicate member pairs.
	rep, err := p.RunSimilarityPass(context.Background(), 0.90, 0.999, DensifyParams{Threshold: 0.95, K: 2, EdgeBudget: 1000})
	require.NoError(t, err)

	// Densify edges were written via exactly ONE create_batch despite the nil LLM.
	require.NotEmpty(t, gc.densifyEdges, "a nil LLM must NOT suppress densify — vector-based edges must be written")
	assert.Equal(t, 1, gc.densifyBatchCalls, "all densify edges ride exactly one create_batch Execute")
	assert.Equal(t, len(gc.densifyEdges), rep.DensifyEdgesTotal, "report total matches edges written")
	assert.NotEmpty(t, rep.DensifyPerTopic, "the touched topic's stats are populated")
	assert.Empty(t, rep.DensifySkippedReason, "a live scanner does NOT set the skipped reason")
	// Every written edge carries the densify provenance.
	for _, e := range gc.densifyEdges {
		assert.Equal(t, "topic-densify", e.GetMethod())
		assert.InDelta(t, densifyEdgeConfidence, e.GetConfidence(), 1e-9)
	}
}
