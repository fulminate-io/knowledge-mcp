// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// artifactNode builds an artifact node of the given type with a recognizable name
// (reusing the wiNode "name-<id>" convention via an explicit type), for the artifact-link
// fixtures.
func artifactNode(id string, t kgtypes.NodeType) *knowledgev1.Node {
	return &knowledgev1.Node{Id: id, Type: string(t), SymbolName: "name-" + id}
}

// findingWithAlgo builds a finding node carrying a non-empty `algorithm` metadata key —
// the FORWARD-COMPATIBLE analyzer-finding marker isAnalyzerFinding honors as an OR.
func findingWithAlgo(id, algo string) *knowledgev1.Node {
	n := artifactNode(id, kgtypes.NodeFinding)
	kgtypes.SetValue(n, "algorithm", algo)
	return n
}

// namedFinding builds a finding node with an explicit SymbolName (for the LOAD-BEARING
// analyzer-title-prefix marker, e.g. "Articulation point: X").
func namedFinding(id, name string) *knowledgev1.Node {
	return &knowledgev1.Node{Id: id, Type: string(kgtypes.NodeFinding), SymbolName: name}
}

// resolveArtifactBundle is a small test helper: resolve the shared bundle from the fake's
// seeded graph over the given thought IDs (the same one-resolution the lever threads).
func resolveArtifactBundle(t *testing.T, fake *treeLinkFake, thoughtIDs ...string) treeResolution {
	t.Helper()
	bundle, err := resolveArtifactsAndRoots(context.Background(), fake, thoughtIDs)
	require.NoError(t, err)
	return bundle
}

// TestArtifactLink_Exclusion (FAILS-WHEN-ABSENT, Phase 3 step 1): isAnalyzerFinding gates
// the analyzer-finding exclusion. The LOAD-BEARING marker is the SymbolName analyzer-title
// prefix (no metadata needed); the `algorithm` metadata key is a forward-compatible OR. A
// hand-authored finding with a normal name and no algorithm metadata is NOT excluded, and
// a non-finding node is never an analyzer finding regardless of name/metadata.
func TestArtifactLink_Exclusion(t *testing.T) {
	// LOAD-BEARING: prefix-only fixture (no algorithm metadata) IS excluded.
	assert.True(t, isAnalyzerFinding(namedFinding("f1", "Articulation point: store.Open")),
		"a finding whose SymbolName starts with the analyzer title prefix is excluded (prefix = load-bearing)")

	// FORWARD-COMPATIBLE: a finding carrying non-empty `algorithm` metadata IS excluded.
	assert.True(t, isAnalyzerFinding(findingWithAlgo("f2", "articulation")),
		"a finding carrying non-empty algorithm metadata is excluded (forward-compatible OR)")

	// A hand-authored finding with a normal name and no algorithm metadata is NOT excluded.
	assert.False(t, isAnalyzerFinding(artifactNode("f3", kgtypes.NodeFinding)),
		"a normal hand-authored finding is NOT an analyzer finding")

	// An empty algorithm value does not trip the metadata branch.
	emptyAlgo := artifactNode("f4", kgtypes.NodeFinding)
	kgtypes.SetValue(emptyAlgo, "algorithm", "")
	assert.False(t, isAnalyzerFinding(emptyAlgo),
		"an empty algorithm value is not a marker")

	// A non-finding node (e.g. a decision) is never an analyzer finding, even if named like one.
	assert.False(t, isAnalyzerFinding(&knowledgev1.Node{Id: "d2", Type: string(kgtypes.NodeDecision), SymbolName: "Articulation point: x"}),
		"a decision named like an analyzer finding is NOT excluded (the marker is finding-only)")
	assert.False(t, isAnalyzerFinding(nil), "nil is not an analyzer finding")
}

// TestArtifactLink_SharedRealDecision_Links (FAILS-WHEN-ABSENT, ticket bullet 1): two
// thoughts each attached (relates-to / informed-by, both directions) to the SAME
// standalone decision that does NOT resolve to a work-item root link as a clique pair.
func TestArtifactLink_SharedRealDecision_Links(t *testing.T) {
	fake := &treeLinkFake{
		edges: []*knowledgev1.Edge{
			relatesEdge("tA", "dec1"),    // tA --relates-to--> dec1
			informedByEdge("tB", "dec1"), // tB --informed-by--> dec1 (other direction/edge type)
		},
		nodes: map[string]*knowledgev1.Node{
			"dec1": artifactNode("dec1", kgtypes.NodeDecision), // standalone — no contains parent
		},
	}
	bundle := resolveArtifactBundle(t, fake, "tA", "tB")
	pairs := computeArtifactLinkEdges(bundle, map[string]bool{})

	require.Len(t, pairs, 1, "two thoughts sharing a standalone decision form exactly one clique pair")
	assert.Equal(t, "tA", pairs[0].A)
	assert.Equal(t, "tB", pairs[0].B)
}

// TestArtifactLink_AnalyzerFinding_DoesNotLink (FAILS-WHEN-ABSENT, ticket bullet 2): two
// thoughts sharing an analyzer finding produce ZERO edges. The fixture uses the
// PREFIX-ONLY marker (SymbolName "Articulation point: X", no algorithm metadata) — the
// load-bearing exclusion path.
func TestArtifactLink_AnalyzerFinding_DoesNotLink(t *testing.T) {
	fake := &treeLinkFake{
		edges: []*knowledgev1.Edge{
			relatesEdge("tA", "af1"),
			relatesEdge("tB", "af1"),
		},
		nodes: map[string]*knowledgev1.Node{
			"af1": namedFinding("af1", "Articulation point: store.Open"), // prefix-only, no metadata
		},
	}
	bundle := resolveArtifactBundle(t, fake, "tA", "tB")
	pairs := computeArtifactLinkEdges(bundle, map[string]bool{})
	assert.Empty(t, pairs, "an analyzer finding (prefix-only marker) yields zero artifact-link pairs")
}

// TestArtifactLink_WorkItemRootedArtifact_DoesNotDoubleLink (FAILS-WHEN-ABSENT, ticket
// bullet 3): two thoughts sharing a finding that a TICKET contains (resolves to a
// work-item root) produce ZERO artifact-link edges — that is tree-link's domain.
func TestArtifactLink_WorkItemRootedArtifact_DoesNotDoubleLink(t *testing.T) {
	fake := &treeLinkFake{
		edges: []*knowledgev1.Edge{
			relatesEdge("tA", "find1"),
			relatesEdge("tB", "find1"),
			containsEdge("ticket1", "find1"), // find1 resolves UP to ticket1 (a work-item root)
		},
		nodes: map[string]*knowledgev1.Node{
			"find1":   artifactNode("find1", kgtypes.NodeFinding),
			"ticket1": wiNode("ticket1", kgtypes.NodeTicket),
		},
	}
	bundle := resolveArtifactBundle(t, fake, "tA", "tB")
	pairs := computeArtifactLinkEdges(bundle, map[string]bool{})
	assert.Empty(t, pairs,
		"a work-item-rooted finding is tree-link's domain — artifact-link does not double-link it")
}

// TestArtifactLink_Idempotent (FAILS-WHEN-ABSENT, ticket bullet 4): a second run with the
// shared pair already present in `existing` writes zero.
func TestArtifactLink_Idempotent(t *testing.T) {
	fake := &treeLinkFake{
		edges: []*knowledgev1.Edge{
			relatesEdge("tA", "dec1"),
			relatesEdge("tB", "dec1"),
		},
		nodes: map[string]*knowledgev1.Node{
			"dec1": artifactNode("dec1", kgtypes.NodeDecision),
		},
	}
	bundle := resolveArtifactBundle(t, fake, "tA", "tB")

	first := computeArtifactLinkEdges(bundle, map[string]bool{})
	require.Len(t, first, 1, "the first run produces the clique pair")

	// Second run with the pair already in the existing set → zero.
	existing := map[string]bool{unorderedPairKey("tA", "tB"): true}
	second := computeArtifactLinkEdges(bundle, existing)
	assert.Empty(t, second, "an all-present existing set yields zero new edges (idempotent)")
}

// TestArtifactLink_Determinism (FAILS-WHEN-ABSENT, ticket bullet 5): identical inputs
// produce a byte-identical ordered pair slice across repeated runs, and a 3-thought
// artifact yields the canonical 3-pair clique in sorted order.
func TestArtifactLink_Determinism(t *testing.T) {
	fake := &treeLinkFake{
		edges: []*knowledgev1.Edge{
			relatesEdge("tC", "dec1"),
			relatesEdge("tA", "dec1"),
			relatesEdge("tB", "dec1"),
		},
		nodes: map[string]*knowledgev1.Node{
			"dec1": artifactNode("dec1", kgtypes.NodeDecision),
		},
	}
	bundle := resolveArtifactBundle(t, fake, "tC", "tA", "tB")

	run1 := computeArtifactLinkEdges(bundle, map[string]bool{})
	run2 := computeArtifactLinkEdges(bundle, map[string]bool{})

	// 3 thoughts → N(N-1)/2 = 3 canonical pairs, sorted.
	want := []artifactLinkCandidate{{A: "tA", B: "tB"}, {A: "tA", B: "tC"}, {A: "tB", B: "tC"}}
	assert.Equal(t, want, run1, "the clique is the canonical sorted 3-pair set")
	assert.Equal(t, run1, run2, "identical inputs produce a byte-identical ordered slice")
}

// TestArtifactLink_MetadataStamps (FAILS-WHEN-ABSENT, ticket bullet 6): every written
// artifact-link edge carries Type=relates-to AND Method=artifact-link AND Confidence=0.25,
// and all ride exactly ONE create_batch Execute; an empty pair set performs no Execute.
func TestArtifactLink_MetadataStamps(t *testing.T) {
	fake := &treeLinkFake{
		edges: []*knowledgev1.Edge{
			relatesEdge("tA", "dec1"),
			relatesEdge("tB", "dec1"),
		},
		nodes: map[string]*knowledgev1.Node{
			"dec1": artifactNode("dec1", kgtypes.NodeDecision),
		},
	}
	p := &PropagationLoop{gc: fake, stopCh: make(chan struct{})}
	bundle := resolveArtifactBundle(t, fake, "tA", "tB")
	rep := &SimilarityReport{}

	p.runArtifactLinkPhase(context.Background(), bundle, rep)

	require.Len(t, fake.artifactWrote, 1, "the single clique edge was written")
	assert.Equal(t, 1, fake.artifactBatch, "all artifact-link edges ride exactly ONE create_batch Execute")
	for _, e := range fake.artifactWrote {
		assert.Equal(t, string(kgtypes.EdgeRelatesTo), e.GetType(), "artifact-link edges are relates-to")
		assert.Equal(t, artifactLinkMethod, e.GetMethod(), "artifact-link edges carry the artifact-link Method")
		assert.InDelta(t, artifactLinkEdgeConfidence, e.GetConfidence(), 1e-9, "artifact-link edges carry Confidence 0.25")
	}

	// An empty pair set is a no-op (no Execute).
	empty := &treeLinkFake{}
	n0, err := writeArtifactLinkEdges(context.Background(), empty, nil)
	require.NoError(t, err)
	assert.Equal(t, 0, n0)
	assert.Equal(t, 0, empty.artifactBatch, "an empty pair set writes nothing")
}

// TestArtifactLink_ReportLines (FAILS-WHEN-ABSENT, ticket bullet 7): fillArtifactLinkReport
// emits one ArtifactLinkStat per qualifying artifact (name + thought count + edges this
// pass) and the run total. A 3-thought shared decision yields one stat with ThoughtCount 3
// and EdgesWritten 3.
func TestArtifactLink_ReportLines(t *testing.T) {
	fake := &treeLinkFake{
		edges: []*knowledgev1.Edge{
			relatesEdge("tA", "dec1"),
			relatesEdge("tB", "dec1"),
			relatesEdge("tC", "dec1"),
		},
		nodes: map[string]*knowledgev1.Node{
			"dec1": artifactNode("dec1", kgtypes.NodeDecision),
		},
	}
	p := &PropagationLoop{gc: fake, stopCh: make(chan struct{})}
	bundle := resolveArtifactBundle(t, fake, "tA", "tB", "tC")
	rep := &SimilarityReport{}

	p.runArtifactLinkPhase(context.Background(), bundle, rep)

	require.Len(t, rep.ArtifactLinkPerArtifact, 1, "one stat for the single grouping artifact")
	st := rep.ArtifactLinkPerArtifact[0]
	assert.Equal(t, "dec1", st.ArtifactID)
	assert.Equal(t, "name-dec1", st.ArtifactName, "the artifact name is loud in the report")
	assert.Equal(t, 3, st.ThoughtCount, "all three thoughts are grouped under the decision")
	assert.Equal(t, 3, st.EdgesWritten, "the 3-thought clique wrote 3 edges this pass")
	assert.Equal(t, 3, rep.ArtifactLinkEdgesTotal, "the run total is the clique edge count")
}

// TestArtifactLink_Compute_Exclusions (FAILS-WHEN-ABSENT): in one fixture mixing a
// qualifying standalone decision, an analyzer finding, and a work-item-rooted finding,
// ONLY the standalone decision's clique survives the 3-way gate.
func TestArtifactLink_Compute_Exclusions(t *testing.T) {
	fake := &treeLinkFake{
		edges: []*knowledgev1.Edge{
			// Qualifying: standalone decision shared by tA, tB.
			relatesEdge("tA", "dec1"),
			relatesEdge("tB", "dec1"),
			// Excluded: analyzer finding shared by tA, tB (prefix marker).
			relatesEdge("tA", "af1"),
			relatesEdge("tB", "af1"),
			// Excluded: work-item-rooted finding shared by tA, tB.
			relatesEdge("tA", "find1"),
			relatesEdge("tB", "find1"),
			containsEdge("ticket1", "find1"),
			// Not a real-artifact type: a thought↔thought edge is never attachment anyway.
		},
		nodes: map[string]*knowledgev1.Node{
			"dec1":    artifactNode("dec1", kgtypes.NodeDecision),
			"af1":     namedFinding("af1", "Articulation point: x"),
			"find1":   artifactNode("find1", kgtypes.NodeFinding),
			"ticket1": wiNode("ticket1", kgtypes.NodeTicket),
		},
	}
	bundle := resolveArtifactBundle(t, fake, "tA", "tB")
	pairs := computeArtifactLinkEdges(bundle, map[string]bool{})
	require.Len(t, pairs, 1, "only the standalone decision's clique survives all three exclusions")
	assert.Equal(t, artifactLinkCandidate{A: "tA", B: "tB"}, pairs[0])
}

// TestArtifactLink_Phase_WriteFailure (FAILS-WHEN-ABSENT): a forced write failure during
// runArtifactLinkPhase records a StageError and the phase returns WITHOUT panic; the
// per-artifact stat is STILL populated (degrade-and-continue).
func TestArtifactLink_Phase_WriteFailure(t *testing.T) {
	fake := &treeLinkFake{
		edges: []*knowledgev1.Edge{
			relatesEdge("tA", "dec1"),
			relatesEdge("tB", "dec1"),
		},
		nodes: map[string]*knowledgev1.Node{
			"dec1": artifactNode("dec1", kgtypes.NodeDecision),
		},
	}
	// Resolve the bundle BEFORE arming the write failure (resolution is read-only; the
	// failure must hit only the edge write, not the resolution reads).
	bundle := resolveArtifactBundle(t, fake, "tA", "tB")
	fake.writeErr = errors.New("forced artifact-link write failure")

	p := &PropagationLoop{gc: fake, stopCh: make(chan struct{})}
	rep := &SimilarityReport{}

	require.NotPanics(t, func() { p.runArtifactLinkPhase(context.Background(), bundle, rep) },
		"a write failure must not panic — the phase degrades and continues")
	require.NotEmpty(t, rep.StageErrors, "the forced write failure is recorded as a StageError")
	assert.Contains(t, rep.StageErrors[0], "artifact-link edge write failed")

	// The per-artifact stat is still populated even when the write failed.
	require.Len(t, rep.ArtifactLinkPerArtifact, 1, "the grouping artifact is still reported")
	assert.Equal(t, 2, rep.ArtifactLinkPerArtifact[0].ThoughtCount, "the decision groups both thoughts")
}
