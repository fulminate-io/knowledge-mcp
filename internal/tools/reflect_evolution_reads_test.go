// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/paging"
)

// evolutionReadRecorder records the wire traffic the evolution handler issues, so a
// test can assert on the READS themselves rather than on the rendered output.
//
// Its classification shape is reused from chargeHydrateRecorder
// (thought_ondemand_corpus_test.go): bucket by return mode and by the REQUESTED
// edge-type set, and carry CATCH-ALL ARMS that record "UNEXPECTED:..." for any shape
// the fixture does not model — a by-id singleton read, or a browse of any node type
// other than thought. Those arms are what make a zero mean "none happened" rather than "none
// are observable", and they must actually EXIST: a recorder that promises a catch-all
// and never writes one turns every downstream no-UNEXPECTED assertion vacuous.
//
// Four shapes are EXPECTED and are not flagged: the nil-query mutation plan (the
// cluster-assignment writeback), a RETURN_MODE_EDGES read, an ids[] hydrate, and the
// type=thought browse — first page or any cursored follow-up page.
//
// The browse arm tests POSITIVELY for type=thought rather than merely for a non-empty
// selection: the weaker form would silently answer a browse of any other type with the
// thought corpus.
//
// It cannot delegate to that recorder: chargeHydrateRecorder's edge arm returns an
// EMPTY response for any type set that is not exactly {charged-by}, which would
// starve the adjacency composition this test drives.
type evolutionReadRecorder struct {
	mu sync.Mutex

	// edgeReadTypes records the requested edge-type set of every RETURN_MODE_EDGES
	// request, in order. Its LENGTH is the logical edge-read count: the fixture is far
	// under paging.EdgePivotPageSize, so one Execute is one logical read.
	edgeReadTypes [][]string
	// mutations counts non-query plans — persistClusterAssignments' bulk metadata
	// write is the one this fixture expects.
	mutations int
	// events records by-id hydrates and anything unrecognized.
	events []string

	thoughts  []*knowledgev1.Node
	nodesByID map[string]*knowledgev1.Node
	edges     []*knowledgev1.Edge
}

func (r *evolutionReadRecorder) record(s string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, s)
}

func (r *evolutionReadRecorder) recorded() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.events...)
}

func (r *evolutionReadRecorder) edgeReads() [][]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([][]string(nil), r.edgeReadTypes...)
}

func (r *evolutionReadRecorder) mutationCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.mutations
}

func (r *evolutionReadRecorder) Execute(
	_ context.Context, req *knowledgev1.ExecuteRequest,
) (*knowledgev1.ExecuteResponse, error) {
	q := req.GetQuery()
	if q == nil {
		// The cluster-assignment writeback (persistClusterAssignments). Counted, not
		// flagged: it is the proof the cold-cluster recompute actually ran.
		r.mu.Lock()
		r.mutations++
		r.mu.Unlock()
		return &knowledgev1.ExecuteResponse{}, nil
	}

	if q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_EDGES {
		types := q.GetSelection().GetEdgeTypes()
		r.mu.Lock()
		r.edgeReadTypes = append(r.edgeReadTypes, append([]string(nil), types...))
		r.mu.Unlock()

		// UNION semantics, matching adjFakeCaller: return every seeded edge whose type
		// is in the requested set; an empty set means every type.
		want := make(map[string]bool, len(types))
		for _, t := range types {
			want[t] = true
		}
		var out []*knowledgev1.Edge
		for _, e := range r.edges {
			if len(want) == 0 || want[e.GetType()] {
				out = append(out, e)
			}
		}
		return &knowledgev1.ExecuteResponse{Edges: bandNarrow(out, q)}, nil
	}

	if ids := q.GetIds(); len(ids) > 0 {
		r.record("hydrate:" + strings.Join(ids, ","))
		resp := &knowledgev1.ExecuteResponse{}
		for _, id := range ids {
			if n, ok := r.nodesByID[id]; ok {
				resp.Nodes = append(resp.Nodes, n)
			}
		}
		return resp, nil
	}

	// CATCH-ALL: a by-id singleton read is a shape this fixture does not model.
	if id := q.GetById(); id != "" {
		r.record("UNEXPECTED:by-id read " + id)
		return &knowledgev1.ExecuteResponse{}, nil
	}

	// CATCH-ALL, POSITIVE TEST: the only remaining expected shape is the type=THOUGHT
	// browse. Testing for a non-empty selection instead would let a browse of any OTHER
	// type through and serve it the thought corpus silently — a charge browse would be
	// answered with thoughts. Naming the type this fixture models is what makes the arm
	// useful for drift rather than merely present.
	if nt := q.GetSelection().GetNodeType(); nt != string(kgtypes.NodeThought) {
		r.record("UNEXPECTED:browse of node type " + nt + " (this fixture models type=thought only)")
		return &knowledgev1.ExecuteResponse{}, nil
	}

	// The type=thought browse. One short page terminates the keyset drain, so a
	// cursored follow-up page is served empty rather than re-serving the same rows.
	if q.GetAfterId() != "" || q.GetOffset() > 0 {
		return &knowledgev1.ExecuteResponse{}, nil
	}
	return &knowledgev1.ExecuteResponse{Nodes: r.thoughts}, nil
}

// TestReflectEvolution_ColdClustersIssueOneUnifiedRead is the gate on the evolution
// handler's read width, on the path where no clusters are persisted yet.
//
// WHY AN EXISTING GATE DOES NOT COVER THIS. TestAdjacency_ColdPathIssuesOneEdgeRead
// exercises the thoughts(adjacency) op, which already wraps its source in
// NewReadMemo. It never reaches ComputeScalarEvolution, so it is green whether or not
// this path threads its memo.
//
// THE WIDTH LEG IS THE POINT, NOT THE COUNT. A count-only assertion passes both when
// the single read is the correct seven-type unified read and when it is a narrowed
// one, so the type set is asserted by ElementsMatch against the seven constants that
// make up unifiedPivotEdgeTypes.
func TestReflectEvolution_ColdClustersIssueOneUnifiedRead(t *testing.T) {
	// Three thoughts carrying NO cluster_id: that is what forces the branch under
	// test. DetectPersistedClusters returns ErrClustersNotComputed, fetchClusterContext
	// yields zero clusters, and ComputeScalarEvolution takes its len(clusters)==0 path.
	th := func(id string) *knowledgev1.Node {
		return &knowledgev1.Node{Id: id, Type: string(kgtypes.NodeThought), SymbolName: id}
	}
	t1, t2, t3 := th("t1"), th("t2"), th("t3")
	ch1 := &knowledgev1.Node{Id: "ch1", Type: string(kgtypes.NodeCharge)}

	rec := &evolutionReadRecorder{
		thoughts: []*knowledgev1.Node{t1, t2, t3},
		nodesByID: map[string]*knowledgev1.Node{
			"t1": t1, "t2": t2, "t3": t3, "ch1": ch1,
		},
		// One edge of each kind the collapsed reads look for, so each has something to
		// find and a starved read is distinguishable from a served one.
		edges: []*knowledgev1.Edge{
			{Type: string(kgtypes.EdgeRelatesTo), FromId: "t1", ToId: "t2"},
			{Type: string(kgtypes.EdgeKGContains), FromId: "s1", ToId: "t1"},
			{Type: string(kgtypes.EdgeKGContains), FromId: "s1", ToId: "t2"},
			{Type: string(kgtypes.EdgeChargedBy), FromId: "t1", ToId: "ch1"},
		},
	}

	// corpusThoughts LEFT EMPTY ON PURPOSE: the zero value is COLD, which keeps the
	// thought browse on the wire and makes the memo the only thing that can collapse
	// the reads.
	deps := interceptTestDeps{gc: rec}

	res := handleReflectEvolution(context.Background(), deps, queryReflectArgs{
		ClusterA: "t1", ClusterB: "t2",
	})
	require.False(t, res.IsError, "evolution handler errored: %s", toolResultText(res))

	// (4) No unrecognized traffic — checked first, because an UNEXPECTED event means
	// the recorder mis-served something and every count below is suspect.
	for _, e := range rec.recorded() {
		require.False(t, strings.HasPrefix(e, "UNEXPECTED:"),
			"recorder saw unrecognized traffic: %s (all events: %v)", e, rec.recorded())
	}

	// (3) NON-VACUITY: the cold-cluster recompute actually ran and persisted
	// assignments. Without this, leg 1 is satisfiable by a path that never entered the
	// branch and therefore issued no reads at all.
	assert.Positive(t, rec.mutationCount(),
		"the len(clusters)==0 recompute must have run and persisted cluster assignments — "+
			"otherwise the read count below is measuring a path that was never taken")

	// (1) EXACTLY ONE logical edge read. Before the memo is threaded this is FOUR, not
	// three — observed, and the fourth site was missed by the planning enumeration:
	//   1. the unified adjacency read (fetchAdjacency, nil src)
	//   2. the un-memoized kg-contains sibling read (deriveSessionSiblings)
	//   3. the un-memoized charged-by read (buildClusterObjects -> fetchChargesFor)
	//   4. a SECOND charged-by read from buildScopedChargeCache (personality.go),
	//      which runs after the cluster recompute and finds no populated memo either
	// All four collapse to one once DetectThoughtClusters threads src: the memo is
	// populated by read 1, and charged-by is inside unifiedPivotEdgeTypes.
	reads := rec.edgeReads()
	// ONE LOGICAL read, issued as exactly paging.EdgeBandCount banded Executes — the
	// unified read is a banded match-all sweep now, so the counter (which counts
	// round-trips) scales with the band count. Asserted against the CONSTANT so a
	// change to it moves this with it; exact equality still catches a consumer that
	// quietly re-acquired its own read, and also catches a band that saturated and split.
	require.Len(t, reads, paging.EdgeBandCount,
		"the evolution path must issue exactly ONE bulk edge read (EdgeBandCount banded Executes); got %d: %v",
		len(reads), reads)

	// (2) WIDTH: that one read is the full seven-type unified set. A narrowed read and
	// a widened one both fail here; a count-only assertion passes in both states.
	assert.ElementsMatch(t, []string{
		string(kgtypes.EdgeNext),
		string(kgtypes.EdgeBranchesFrom),
		string(kgtypes.EdgeRelatesTo),
		string(kgtypes.EdgeProduced),
		string(kgtypes.EdgeBecause),
		string(kgtypes.EdgeKGContains),
		string(kgtypes.EdgeChargedBy),
	}, reads[0], "the single read carries the seven-type unified pivot-edge set")
}

// TestEvolutionRecorder_CatchAllArmsRecordUnexpected is the DISCRIMINATING gate on the
// recorder itself: it proves the catch-all arms exist and fire.
//
// It exists because every other criterion on this step passed at a snapshot where the
// arms were only PROMISED by a doc comment and never written. Under that recorder the
// no-unexpected-events leg of the main test, and the artifact grep guard, were both
// vacuous — they asserted the absence of a record that nothing could ever produce.
// Asserting the arms fire is what makes their silence elsewhere mean something.
func TestEvolutionRecorder_CatchAllArmsRecordUnexpected(t *testing.T) {
	ctx := context.Background()
	newRec := func() *evolutionReadRecorder {
		return &evolutionReadRecorder{
			thoughts:  []*knowledgev1.Node{{Id: "t1", Type: string(kgtypes.NodeThought)}},
			nodesByID: map[string]*knowledgev1.Node{},
		}
	}
	query := func(q *knowledgev1.QueryPlan) *knowledgev1.ExecuteRequest {
		return &knowledgev1.ExecuteRequest{Plan: &knowledgev1.ExecuteRequest_Query{Query: q}}
	}

	t.Run("by-id read is flagged", func(t *testing.T) {
		rec := newRec()
		_, err := rec.Execute(ctx, query(&knowledgev1.QueryPlan{ById: "some-node"}))
		require.NoError(t, err)
		require.Len(t, rec.recorded(), 1)
		assert.Contains(t, rec.recorded()[0], "UNEXPECTED:by-id read some-node")
	})

	t.Run("browse of a non-thought type is flagged", func(t *testing.T) {
		rec := newRec()
		_, err := rec.Execute(ctx, query(&knowledgev1.QueryPlan{
			Selection: &knowledgev1.Selection{NodeType: string(kgtypes.NodeCharge)},
		}))
		require.NoError(t, err)
		require.Len(t, rec.recorded(), 1)
		assert.Contains(t, rec.recorded()[0], "UNEXPECTED:browse of node type charge")
	})

	// KNOWN-POSITIVE CONTROL: the shape the fixture DOES model records nothing, so the
	// arms above are discriminating rather than firing on everything.
	t.Run("the modeled thought browse is NOT flagged", func(t *testing.T) {
		rec := newRec()
		resp, err := rec.Execute(ctx, query(&knowledgev1.QueryPlan{
			Selection: &knowledgev1.Selection{NodeType: string(kgtypes.NodeThought)},
		}))
		require.NoError(t, err)
		assert.Empty(t, rec.recorded(), "the modeled browse must not be flagged")
		assert.Len(t, resp.GetNodes(), 1, "and it is served the seeded corpus")
	})
}
