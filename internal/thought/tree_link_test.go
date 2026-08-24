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

// treeLinkFake serves the reads the tree-link phase drives and records the writes:
//   - RETURN_MODE_EDGES query → the seeded contains/relates-to edges (the helper
//     re-filters by direction+type, so returning the whole seeded set is safe);
//   - ids[] node hydrate → the seeded nodes (id → type/name);
//   - mutate(create_batch) → records every tree-link edge (Method=tree-link).
//
// writeErr, when set, makes every mutation Execute fail (the stage-error path).
type treeLinkFake struct {
	edges         []*knowledgev1.Edge
	nodes         map[string]*knowledgev1.Node
	wrote         []*knowledgev1.BatchEdgeSpec // recorded tree-link edges across all batches
	batchCall     int                          // create_batch Executes carrying tree-link edges
	artifactWrote []*knowledgev1.BatchEdgeSpec // recorded artifact-link edges across all batches
	artifactBatch int                          // create_batch Executes carrying artifact-link edges
	writeErr      error
}

func (f *treeLinkFake) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	if m := req.GetMutation(); m != nil {
		if f.writeErr != nil {
			return nil, f.writeErr
		}
		var tl, al []*knowledgev1.BatchEdgeSpec
		for _, e := range m.GetEdges() {
			switch e.GetMethod() {
			case treeLinkMethod:
				tl = append(tl, e)
			case artifactLinkMethod:
				al = append(al, e)
			}
		}
		if len(tl) > 0 {
			f.batchCall++
			f.wrote = append(f.wrote, tl...)
		}
		if len(al) > 0 {
			f.artifactBatch++
			f.artifactWrote = append(f.artifactWrote, al...)
		}
		return &knowledgev1.ExecuteResponse{}, nil
	}
	q := req.GetQuery()
	if q == nil {
		return &knowledgev1.ExecuteResponse{}, nil
	}
	if q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_EDGES {
		return &knowledgev1.ExecuteResponse{Edges: bandNarrow(f.edges, q)}, nil
	}
	if len(q.GetIds()) > 0 {
		var out []*knowledgev1.Node
		for _, id := range q.GetIds() {
			if n, ok := f.nodes[id]; ok {
				out = append(out, n)
			}
		}
		return &knowledgev1.ExecuteResponse{Nodes: out}, nil
	}
	return &knowledgev1.ExecuteResponse{}, nil
}

// containsEdge is a parent→child contains edge (EdgeKGContains is parent→child).
func containsEdge(parent, child string) *knowledgev1.Edge {
	return &knowledgev1.Edge{Type: string(kgtypes.EdgeKGContains), FromId: parent, ToId: child}
}

// relatesEdge is a general from→to relates-to edge (the authored links: param shape:
// thought--relates-to-->artifact).
func relatesEdge(from, to string) *knowledgev1.Edge {
	return &knowledgev1.Edge{Type: string(kgtypes.EdgeRelatesTo), FromId: from, ToId: to}
}

// informedByEdge is a from--informed-by-->to edge (another non-contains attachment shape).
func informedByEdge(from, to string) *knowledgev1.Edge {
	return &knowledgev1.Edge{Type: string(kgtypes.EdgeInformedBy), FromId: from, ToId: to}
}

// wiNode builds a node of the given work-item / structural type.
func wiNode(id string, t kgtypes.NodeType) *knowledgev1.Node {
	return &knowledgev1.Node{Id: id, Type: string(t), SymbolName: "name-" + id}
}

// TestTreeLink_CollectArtifacts (FAILS-WHEN-ABSENT): attachment is edge-type-UNFILTERED
// and BIDIRECTIONAL. A thought contained by a step AND (separately) by a project
// surfaces BOTH; a thought that merely relates-to a finding (FROM the thought, a
// non-contains edge — the historical links: shape) surfaces that finding; an
// informed-by edge attaches likewise. A thought attached ONLY via thought_session /
// proxy / charge yields ZERO artifacts (those neighbor types stay excluded), and a
// thought↔thought relates-to edge is NOT attachment (the other thought is dropped).
func TestTreeLink_CollectArtifacts(t *testing.T) {
	fake := &treeLinkFake{
		edges: []*knowledgev1.Edge{
			containsEdge("step1", "tA"),     // step contains thought tA (artifact→thought)
			containsEdge("proj1", "tA"),     // project also directly contains tA
			relatesEdge("tD", "finding1"),   // tD --relates-to--> finding1 (thought→artifact, NON-contains)
			informedByEdge("tD", "ticket9"), // tD --informed-by--> ticket9 (another non-contains attachment)
			relatesEdge("tA", "tD"),         // tA --relates-to--> tD : thought↔thought, NOT attachment
			containsEdge("sess1", "tB"),     // thought_session contains tB (excluded)
			containsEdge("proxy1", "tB"),    // proxy contains tB (excluded)
			containsEdge("charge1", "tC"),   // charge contains tC (excluded)
		},
		nodes: map[string]*knowledgev1.Node{
			"step1":    wiNode("step1", kgtypes.NodeStep),
			"proj1":    wiNode("proj1", kgtypes.NodeProject),
			"finding1": wiNode("finding1", kgtypes.NodeFinding),
			"ticket9":  wiNode("ticket9", kgtypes.NodeTicket),
			"tD":       wiNode("tD", kgtypes.NodeThought),
			"sess1":    wiNode("sess1", kgtypes.NodeThoughtSession),
			"proxy1":   wiNode("proxy1", kgtypes.NodeProxy),
			"charge1":  wiNode("charge1", kgtypes.NodeCharge),
		},
	}
	got, artifactByID, err := collectThoughtArtifacts(context.Background(), fake, []string{"tA", "tB", "tC", "tD"})
	require.NoError(t, err)

	assert.ElementsMatch(t, []string{"proj1", "step1"}, got["tA"],
		"tA surfaces both its step and project artifacts; the thought↔thought relates-to to tD is NOT attachment")
	assert.ElementsMatch(t, []string{"finding1", "ticket9"}, got["tD"],
		"tD attaches to its finding (relates-to) and ticket (informed-by) — non-contains edges count, both directions")
	assert.Empty(t, got["tB"], "a thought attached only by a session/proxy yields zero artifacts (session exclusion)")
	assert.Empty(t, got["tC"], "a charge parent is not a work-item artifact")

	// artifactByID carries ONLY the kept artifacts (hydrated nodes), keyed by id, with
	// every dropped neighbor type (thought/session/proxy/charge) absent — the in-hand
	// hydrate the artifact-link phase reuses with no third bulk read.
	assert.ElementsMatch(t, []string{"step1", "proj1", "finding1", "ticket9"},
		keysOf(artifactByID),
		"artifactByID holds exactly the kept artifacts; tD (thought), sess1, proxy1, charge1 are absent")
	require.Contains(t, artifactByID, "finding1")
	assert.Equal(t, string(kgtypes.NodeFinding), artifactByID["finding1"].GetType(),
		"the kept artifact's hydrated type is carried for downstream classification")
}

// keysOf returns the keys of an id→node map (test helper for artifactByID assertions).
func keysOf[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestTreeLink_ResolveRoots (FAILS-WHEN-ABSENT): a thought contained by a
// step→phase→plan→ticket→project chain AND directly by the project both resolve to the
// SAME project root; an artifact with no contains-parent is its own root; a contains
// CYCLE (a→b→a) terminates deterministically (no hang) choosing the min-ID node; a
// thought whose only artifact resolves to a NON-work-item root is dropped.
func TestTreeLink_ResolveRoots(t *testing.T) {
	fake := &treeLinkFake{
		edges: []*knowledgev1.Edge{
			// tA's containment-adjacent artifact is the deep step; the spine resolves up.
			containsEdge("step1", "tA"),
			containsEdge("phase1", "step1"),
			containsEdge("plan1", "phase1"),
			containsEdge("ticket1", "plan1"),
			containsEdge("proj1", "ticket1"),
			// tA is ALSO directly under the project — same root, exercises the tie-break.
			containsEdge("proj1", "tA"),
			// tB attaches to finding cycA, which lives under a `contains` CYCLE
			// (cycA→cycB→cycA) that climbs to no work-item root → tB is dropped. cycA is a
			// finding (a kept, non-thought neighbor) so the attachment survives and the
			// ancestry walk genuinely exercises the cycle guard.
			containsEdge("cycA", "tB"),
			containsEdge("cycA", "cycB"),
			containsEdge("cycB", "cycA"),
		},
		nodes: map[string]*knowledgev1.Node{
			"step1":   wiNode("step1", kgtypes.NodeStep),
			"phase1":  wiNode("phase1", kgtypes.NodePhase),
			"plan1":   wiNode("plan1", kgtypes.NodePlan),
			"ticket1": wiNode("ticket1", kgtypes.NodeTicket),
			"proj1":   wiNode("proj1", kgtypes.NodeProject),
			// cycA/cycB are findings (non-work-item types) → no eligible root, but they
			// ARE kept neighbors so the cycle guard in the ancestry walk is exercised.
			"cycA": wiNode("cycA", kgtypes.NodeFinding),
			"cycB": wiNode("cycB", kgtypes.NodeFinding),
		},
	}
	rootByThought, rootNames, err := ResolveTreeRoots(context.Background(), fake, []string{"tA", "tB"})
	require.NoError(t, err) // a cycle must NOT hang or error

	assert.Equal(t, "proj1", rootByThought["tA"],
		"both the deep-spine artifact and the direct-project artifact resolve to the project root")
	assert.Equal(t, "name-proj1", rootNames["proj1"], "the root name is hydrated for the report")
	_, ok := rootByThought["tB"]
	assert.False(t, ok, "a thought whose artifacts resolve to no work-item root writes nothing")
}

// TestTreeLink_ResolveRoots_MissingParentIsOwnRoot (FAILS-WHEN-ABSENT): an artifact that
// is itself a work-item with NO contains-parent is its own tree root.
func TestTreeLink_ResolveRoots_MissingParentIsOwnRoot(t *testing.T) {
	fake := &treeLinkFake{
		edges: []*knowledgev1.Edge{
			containsEdge("ticketX", "tA"), // ticketX has no parent → own root
		},
		nodes: map[string]*knowledgev1.Node{
			"ticketX": wiNode("ticketX", kgtypes.NodeTicket),
		},
	}
	rootByThought, _, err := ResolveTreeRoots(context.Background(), fake, []string{"tA"})
	require.NoError(t, err)
	assert.Equal(t, "ticketX", rootByThought["tA"],
		"a parentless work-item artifact is its own tree root")
}

// TestTreeLink_ResolveRoots_RelatesToTicketIsMember (FAILS-WHEN-ABSENT, the CEO ruling's
// core widening): a thought attached ONLY by a non-contains edge (relates-to FROM the
// thought TO a ticket) is a member of that ticket's tree. This FAILS on the pre-change
// contains-only attachment read (which found zero non-contains attachments — the
// 5,825-thought corpus → 0 trees measurement). The companion case: a thought attached
// only by relates-to to ANOTHER THOUGHT resolves to NO tree (thought↔thought is the
// similarity/densify domain, not attachment).
func TestTreeLink_ResolveRoots_RelatesToTicketIsMember(t *testing.T) {
	fake := &treeLinkFake{
		edges: []*knowledgev1.Edge{
			// tA merely relates-to ticketY directly — no contains edge touches tA at all.
			relatesEdge("tA", "ticketY"),
			// tB relates-to a finding that ticketY contains — attachment via the finding,
			// spine via the contains edge → tB is also a ticketY member.
			relatesEdge("tB", "find1"),
			containsEdge("ticketY", "find1"),
			// tC relates-to ONLY another thought → no work-item artifact → no tree.
			relatesEdge("tC", "tOther"),
		},
		nodes: map[string]*knowledgev1.Node{
			"ticketY": wiNode("ticketY", kgtypes.NodeTicket),
			"find1":   wiNode("find1", kgtypes.NodeFinding),
			"tOther":  wiNode("tOther", kgtypes.NodeThought),
		},
	}
	rootByThought, rootNames, err := ResolveTreeRoots(context.Background(), fake, []string{"tA", "tB", "tC"})
	require.NoError(t, err)

	assert.Equal(t, "ticketY", rootByThought["tA"],
		"a thought attached ONLY via relates-to to a ticket is a member of that ticket's tree")
	assert.Equal(t, "ticketY", rootByThought["tB"],
		"a thought relates-to a finding the ticket contains → resolves up the spine to the ticket tree")
	assert.Equal(t, "name-ticketY", rootNames["ticketY"], "the shared tree root name is hydrated")
	_, ok := rootByThought["tC"]
	assert.False(t, ok, "a thought attached only to another thought (relates-to) resolves to no tree")
}

// TestTreeLink_CliqueCompute (FAILS-WHEN-ABSENT): N tree thoughts → exactly N(N-1)/2
// canonical pairs (clique completeness); a single-thought tree yields zero pairs; a
// second run with all pairs already in `existing` writes ZERO (idempotency); identical
// inputs produce a byte-identical edge set in identical order (determinism).
func TestTreeLink_CliqueCompute(t *testing.T) {
	// One tree (root R) with 4 thoughts → 4*3/2 = 6 pairs; a lone tree (root S, 1
	// thought) contributes nothing.
	roots := map[string]string{
		"a": "R", "b": "R", "c": "R", "d": "R",
		"z": "S",
	}
	pairs := computeTreeLinkEdges(roots, map[string]bool{})
	require.Len(t, pairs, 6, "4-thought tree → exactly N(N-1)/2 = 6 clique pairs; the 1-thought tree adds none")
	for _, p := range pairs {
		assert.Less(t, p.A, p.B, "every clique pair is canonical (A < B)")
	}

	// Determinism: a re-run yields the identical ordered slice.
	pairs2 := computeTreeLinkEdges(roots, map[string]bool{})
	require.Equal(t, pairs, pairs2, "computeTreeLinkEdges must be deterministic across runs")

	// Idempotency: with every pair already present, a re-run emits zero.
	existing := map[string]bool{}
	for _, p := range pairs {
		existing[unorderedPairKey(p.A, p.B)] = true
	}
	assert.Empty(t, computeTreeLinkEdges(roots, existing),
		"a second run over an already-linked tree must write zero new edges (idempotency)")

	// Idempotency is direction-insensitive: a B→A existing key blocks the A-B pair.
	half := map[string]bool{unorderedPairKey("b", "a"): true}
	got := computeTreeLinkEdges(map[string]string{"a": "R", "b": "R"}, half)
	assert.Empty(t, got, "an existing edge in EITHER direction suppresses the clique pair")
}

// TestTreeLink_Write (FAILS-WHEN-ABSENT): every written edge carries Type=relates-to,
// Method="tree-link", AND Confidence=treeLinkEdgeConfidence (0.25) — both halves of the
// provenance criterion (mirrors the densify precedent). All edges ride ONE create_batch;
// an empty pair set performs no Execute.
func TestTreeLink_Write(t *testing.T) {
	fake := &treeLinkFake{}
	pairs := []treeLinkCandidate{
		{A: "a1", B: "a2"},
		{A: "b1", B: "b2"},
		{A: "b2", B: "b3"},
	}
	n, err := writeTreeLinkEdges(context.Background(), fake, pairs)
	require.NoError(t, err)
	assert.Equal(t, 3, n, "three pairs → three edges written")
	assert.Equal(t, 1, fake.batchCall, "all tree-link edges ride exactly ONE create_batch Execute")
	require.Len(t, fake.wrote, 3)
	for _, e := range fake.wrote {
		assert.Equal(t, string(kgtypes.EdgeRelatesTo), e.GetType(), "tree-link edges are relates-to")
		assert.Equal(t, treeLinkMethod, e.GetMethod(), "tree-link edges carry the tree-link Method")
		assert.InDelta(t, treeLinkEdgeConfidence, e.GetConfidence(), 1e-9, "tree-link edges carry Confidence 0.25")
	}

	// An empty pair set is a no-op (no Execute).
	empty := &treeLinkFake{}
	n0, err := writeTreeLinkEdges(context.Background(), empty, nil)
	require.NoError(t, err)
	assert.Equal(t, 0, n0)
	assert.Equal(t, 0, empty.batchCall, "an empty pair set writes nothing")
}

// TestTreeLink_Phase_WriteFailureRecordsStageError (FAILS-WHEN-ABSENT): a forced write
// failure during runTreeLinkPhase records a StageError and the phase returns WITHOUT
// panic (degrade-and-continue, ticket rule 6).
func TestTreeLink_Phase_WriteFailureRecordsStageError(t *testing.T) {
	fake := &treeLinkFake{
		edges: []*knowledgev1.Edge{
			// Two thoughts under one ticket → one clique pair to attempt to write.
			containsEdge("ticket1", "t1"),
			containsEdge("ticket1", "t2"),
		},
		nodes: map[string]*knowledgev1.Node{
			"ticket1": wiNode("ticket1", kgtypes.NodeTicket),
		},
		writeErr: errors.New("forced tree-link write failure"),
	}
	p := &PropagationLoop{gc: fake, stopCh: make(chan struct{})}
	rep := &SimilarityReport{}

	// runTreeLinkPhase now consumes the shared resolution bundle (resolved ONCE by
	// runStructuralCliquePhases in production); build it here so this test exercises the
	// tree-link phase in isolation.
	bundle, berr := resolveArtifactsAndRoots(context.Background(), fake, []string{"t1", "t2"})
	require.NoError(t, berr)

	require.NotPanics(t, func() { p.runTreeLinkPhase(context.Background(), bundle, rep) },
		"a write failure must not panic — the phase degrades and continues")
	require.NotEmpty(t, rep.StageErrors, "the forced write failure must be recorded as a StageError")
	assert.Contains(t, rep.StageErrors[0], "tree-link edge write failed")

	// The phase still grouped the tree (the per-tree stat is populated even when the
	// write failed) so the report stays loud about the tree it attempted.
	require.Len(t, rep.TreeLinkPerTree, 1, "the grouping tree is still reported")
	assert.Equal(t, 2, rep.TreeLinkPerTree[0].ThoughtCount, "the ticket tree groups both thoughts")
}

// TestTreeLink_Phase_HappyPath (FAILS-WHEN-ABSENT): two thoughts under one ticket are
// resolved, the single clique edge is written via one batch, and the report's per-tree
// stat + total are filled with the tree root name.
func TestTreeLink_Phase_HappyPath(t *testing.T) {
	fake := &treeLinkFake{
		edges: []*knowledgev1.Edge{
			containsEdge("ticket1", "t1"),
			containsEdge("ticket1", "t2"),
		},
		nodes: map[string]*knowledgev1.Node{
			"ticket1": wiNode("ticket1", kgtypes.NodeTicket),
		},
	}
	p := &PropagationLoop{gc: fake, stopCh: make(chan struct{})}
	rep := &SimilarityReport{}

	bundle, berr := resolveArtifactsAndRoots(context.Background(), fake, []string{"t1", "t2"})
	require.NoError(t, berr)
	p.runTreeLinkPhase(context.Background(), bundle, rep)

	assert.Equal(t, 1, rep.TreeLinkEdgesTotal, "one clique edge for the 2-thought tree")
	require.Len(t, rep.TreeLinkPerTree, 1)
	st := rep.TreeLinkPerTree[0]
	assert.Equal(t, "ticket1", st.RootID)
	assert.Equal(t, "name-ticket1", st.RootName)
	assert.Equal(t, 2, st.ThoughtCount)
	assert.Equal(t, 1, st.EdgesWritten)
	require.Len(t, fake.wrote, 1, "the single clique edge rode the batch")
	assert.Equal(t, treeLinkMethod, fake.wrote[0].GetMethod())
}
