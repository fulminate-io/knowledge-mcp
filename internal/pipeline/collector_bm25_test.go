// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// collector_bm25_test.go covers the BM25 arm: its graph gate, its O(1) change
// gate, its ship-then-advance ordering, and its quiescence edge.

// fakeCorpusScanner is the CorpusDelta seam, counting every WALK the arm issues.
//
// THE CALL COUNT IS THE INSTRUMENT the change gate is judged on, deliberately:
// the gate's whole claim is about how many requests a quiescent graph makes, and
// timing a loop would measure the tick interval instead.
type fakeCorpusScanner struct {
	calls int
	reqs  []*knowledgev1.CorpusDeltaRequest
	pages []*knowledgev1.CorpusDeltaResponse
	err   error
}

func (f *fakeCorpusScanner) CorpusDelta(
	_ context.Context, req *knowledgev1.CorpusDeltaRequest,
) (*knowledgev1.CorpusDeltaResponse, error) {
	f.calls++
	f.reqs = append(f.reqs, req)
	if f.err != nil {
		return nil, f.err
	}
	if len(f.pages) == 0 {
		return &knowledgev1.CorpusDeltaResponse{}, nil
	}
	p := f.pages[0]
	f.pages = f.pages[1:]
	return p, nil
}

// bm25TestCollector wires an arm the way RegisterGraph does, over the fake ship
// manager, so the tests exercise the SAME closure shapes production builds.
func bm25TestCollector(
	fsm *fakeShipManager, gt kgtypes.GraphType, name string, stamp func() (int64, bool),
) *collector {
	return &collector{
		gt: gt, name: name,
		flush: func(ctx context.Context) error { return fsm.Flush(ctx, gt, name) },
		bm25: bm25Arm{
			enabled:     true,
			wake:        make(chan struct{}, 1),
			loadCursors: func() ([]*knowledgev1.LayerCursor, error) { return fsm.LoadBM25Cursors(gt, name) },
			saveCursors: func(c []*knowledgev1.LayerCursor) error { return fsm.SaveBM25Cursors(gt, name, c) },
			corpusStamp: stamp,
			ship: func(ctx context.Context, docs []searchengine.Document) error {
				return fsm.AddAndMarkDirtyFields(ctx, gt, name, docs)
			},
			deleteIDs: func(ctx context.Context, ids []searchengine.ExternalID) error {
				return fsm.DeleteFromBuckets(ctx, gt, name, ids)
			},
		},
	}
}

// bm25Page builds one served page: live rows carrying composed fields, plus
// optional tombstoned rows carrying none.
func bm25Page(live []string, tombstoned []string) *knowledgev1.CorpusDeltaResponse {
	resp := &knowledgev1.CorpusDeltaResponse{}
	var lastUpdated int64
	for i, id := range live {
		lastUpdated = int64(i+1) * 1_000_000
		resp.Items = append(resp.Items, &knowledgev1.Node{Id: id, UpdatedAt: lastUpdated})
		resp.Bm25Items = append(resp.Bm25Items, &knowledgev1.CorpusDeltaBm25Item{
			NodeId: id, Fields: &knowledgev1.Bm25Fields{Summary: id + " summary"},
		})
	}
	for i, id := range tombstoned {
		lastUpdated = int64(len(live)+i+1) * 1_000_000
		resp.Items = append(resp.Items, &knowledgev1.Node{
			Id: id, UpdatedAt: lastUpdated, TombstonedAt: lastUpdated,
		})
	}
	resp.SafeHorizon = lastUpdated + 1
	resp.NextCursors = []*knowledgev1.LayerCursor{
		{LayerKey: "default", AfterUpdatedAt: lastUpdated, AfterId: "last"},
	}
	return resp
}

// TestBM25Arm_GraphGateIsHasRebuildableSegments asserts the gate over an EXPLICIT
// enumeration of all eleven builtin graph types rather than a sampled pair.
//
// A SAMPLED PAIR WOULD MISS THE TRAP this gate exists for: transformers is
// Summarizable-but-NOT-Embeddable and gets zero segments today, so a hand-rolled
// "BM25-eligible = not embeddable" predicate would silently grant it segments. The
// full enumeration is what makes that impossible to pass by accident.
//
// ONE NAMED SUBTEST PER TYPE, not one loop over a table, and the difference is what
// a reader of a failure sees: a loop reports "graph type X admitted=true" from a
// parent that also covers ten other types, while a per-type subtest names the type
// in the runner line itself. It is also what lets a gate assert the POPULATION SIZE
// — a table that quietly lost rows fails the subtest count rather than passing on a
// shorter list, which a whole-run assertion cannot distinguish.
//
// THE NO-MANAGER CHECK IS FOLDED INTO EACH TYPE rather than run as a twelfth
// sibling subtest. Both directions belong to the same type, and keeping them
// together is what makes each subtest a complete statement about one graph type.
func TestBM25Arm_GraphGateIsHasRebuildableSegments(t *testing.T) {
	admitted := map[kgtypes.GraphType]bool{
		kgtypes.GraphKnowledge: true,
		kgtypes.GraphCode:      true,
		kgtypes.GraphCloud:     true,
		kgtypes.GraphCICD:      true,
		kgtypes.GraphPractice:  true,
		kgtypes.GraphChecks:    true,
	}
	all := []kgtypes.GraphType{
		kgtypes.GraphKnowledge, kgtypes.GraphCode, kgtypes.GraphCloud, kgtypes.GraphCICD,
		kgtypes.GraphPractice, kgtypes.GraphLinkage, kgtypes.GraphTransformers,
		kgtypes.GraphChecks, kgtypes.GraphLogs, kgtypes.GraphWebRaw, kgtypes.GraphPDFRaw,
	}
	require.Len(t, all, 11, "the enumeration must cover every builtin graph type")

	var admittedCount int
	for _, gt := range all {
		got := bm25ArmEnabledFor(gt, true)
		if got {
			admittedCount++
		}
		t.Run(string(gt), func(t *testing.T) {
			assert.Equal(t, admitted[gt], got, "graph type %q admitted=%v", gt, got)
			// The arm is a producer of segments; with no manager there is nothing to
			// produce into, and starting the loop would be a lane that can never work.
			assert.False(t, bm25ArmEnabledFor(gt, false), "graph type %q with no manager", gt)
		})
	}
	assert.Equal(t, 6, admittedCount, "exactly the six HasRebuildableSegments types are admitted")
}

// TestBM25Arm_RequestsEveryNodeType guards the invariant cursorHighWater's doc calls
// load-bearing — "all_node_types and this fold are one design and not two choices" —
// which until now was stated in prose and asserted by nothing.
//
// IT IS A REQUEST-SHAPE ASSERTION, and it has to be, because THIS SIDE CANNOT SEE THE
// CONSEQUENCE. The node-type filter runs on the SERVER; the fake scanner here returns
// its scripted page whatever the request said. So no client fixture can discriminate
// the axis by its RESULT — a page row carrying an out-of-triple node type would come
// back identically under all_node_types and under a triple filter, which is why one
// was not added: it would read as rigor and measure nothing. What the client owns is
// the request it builds, so that is what is pinned.
//
// THE CONSEQUENCE IS PINNED ON THE OTHER SIDE OF THE SEAM, and the pair is the whole
// coverage: bootstrap's TestCorpusDelta_AllNodeTypesAdmitsTheWholeGraph drives all
// three readings over one code-shaped fixture — empty node_types serves ZERO rows,
// all_node_types serves every row INCLUDING an unknown tree-sitter leaf type, an
// explicit list still filters. These two tests flank a module boundary that nothing
// crosses end to end (separate Go modules, no shared harness), so each names the
// other; neither stands alone and a reader must not mistake either for full coverage.
//
// BOTH FIELDS ARE ASSERTED, not just the flag, and the empty-list leg is not padding:
// sending all_node_types together with a node_types list is a REFUSED input on the
// server (TestCorpusDelta_AllNodeTypesWithNodeTypesIsRefused — it is an error, not a
// precedence rule), so asserting the list stays empty is what keeps this request
// inside the server's accepted input space rather than merely carrying the flag.
//
// WORST REACHABLE CONSEQUENCE if this regresses, which is what sets its weight: a
// node_types filter puts a code graph's tree-sitter leaves outside the request, the
// arm indexes NOTHING for the largest BM25 corpus in the product, and every gate in
// this ticket stays green — the exact silent under-indexing the ticket exists to end.
func TestBM25Arm_RequestsEveryNodeType(t *testing.T) {
	fsm := &fakeShipManager{}
	// stamp reports unsampled so the gate fails open and the drain actually runs.
	c := bm25TestCollector(fsm, kgtypes.GraphCode, "repo", func() (int64, bool) { return 0, false })
	sc := &fakeCorpusScanner{pages: []*knowledgev1.CorpusDeltaResponse{bm25Page([]string{"n1"}, nil)}}

	_, err := c.drainBM25(context.Background(), sc, nil)
	require.NoError(t, err)

	require.NotEmpty(t, sc.reqs,
		"CONTROL: the drain must have issued a request at all, or the assertions below "+
			"are read off a request that was never built")
	assert.True(t, sc.reqs[0].GetAllNodeTypes(),
		"the arm must request EVERY node type — an empty node_types is the thought-corpus "+
			"triple on both backends, not 'everything'")
	assert.Empty(t, sc.reqs[0].GetNodeTypes(),
		"and it must carry NO node_types list: a filter would serve a code graph nothing, "+
			"and the flag plus a list together is a refused input server-side")
}

// TestBM25Arm_QuiescentGraphWalksZeroTimes is the O(1) change gate, and it COUNTS
// WALKS rather than elapsed time — a slower loop must not be able to pass it.
func TestBM25Arm_QuiescentGraphWalksZeroTimes(t *testing.T) {
	const ticks = 5

	t.Run("quiescent_ticks_walk_zero_times", func(t *testing.T) {
		fsm := &fakeShipManager{}
		// The arm's position is already at the server's high-water.
		require.NoError(t, fsm.SaveBM25Cursors(kgtypes.GraphCode, "repo",
			[]*knowledgev1.LayerCursor{{LayerKey: "default", AfterUpdatedAt: 9_000, AfterId: "z"}}))
		c := bm25TestCollector(fsm, kgtypes.GraphCode, "repo",
			func() (int64, bool) { return 9_000, true })

		scanner := &fakeCorpusScanner{}
		for range ticks {
			cursors, err := c.bm25.loadCursors()
			require.NoError(t, err)
			if !c.skipQuiescentGraph(cursors) {
				_, derr := c.drainBM25(context.Background(), scanner, cursors)
				require.NoError(t, derr)
			}
		}
		assert.Zero(t, scanner.calls,
			"a quiescent graph must issue ZERO CorpusDelta requests across %d ticks — the walk cost "+
				"was accepted for change processing only, and an ungated per-tick walk is what the "+
				"ruling forbids", ticks)
	})

	t.Run("a_change_admits_exactly_one_walk", func(t *testing.T) {
		// THE KNOWN POSITIVE. Without it, a loop that never ran, an arm never wired,
		// and a working gate are the same zero above.
		fsm := &fakeShipManager{}
		require.NoError(t, fsm.SaveBM25Cursors(kgtypes.GraphCode, "repo",
			[]*knowledgev1.LayerCursor{{LayerKey: "default", AfterUpdatedAt: 9_000, AfterId: "z"}}))
		// The server has moved PAST the arm's position.
		c := bm25TestCollector(fsm, kgtypes.GraphCode, "repo",
			func() (int64, bool) { return 12_000, true })

		scanner := &fakeCorpusScanner{pages: []*knowledgev1.CorpusDeltaResponse{bm25Page([]string{"n1"}, nil)}}
		cursors, err := c.bm25.loadCursors()
		require.NoError(t, err)
		require.False(t, c.skipQuiescentGraph(cursors), "a stamp past the cursor must open the gate")
		shipped, err := c.drainBM25(context.Background(), scanner, cursors)
		require.NoError(t, err)

		assert.Equal(t, 1, scanner.calls, "one short page terminates the drain in exactly one walk")
		assert.Equal(t, 1, shipped)
	})

	t.Run("an_unsampled_graph_fails_open_and_drains", func(t *testing.T) {
		// "Not yet polled" must NEVER read as "nothing changed": that would keep a
		// freshly-registered graph out of the BM25 corpus until some unrelated event
		// happened to poll it.
		fsm := &fakeShipManager{}
		c := bm25TestCollector(fsm, kgtypes.GraphCode, "repo",
			func() (int64, bool) { return 0, false })
		assert.False(t, c.skipQuiescentGraph(nil), "an unsampled graph must drain, not skip")
	})

	t.Run("a_zero_stamp_on_a_sampled_graph_skips", func(t *testing.T) {
		// Different from the arm above and the distinction matters: a sampled zero is
		// the server's honest "never recorded", and an empty graph has nothing to drain.
		fsm := &fakeShipManager{}
		c := bm25TestCollector(fsm, kgtypes.GraphCode, "repo",
			func() (int64, bool) { return 0, true })
		assert.True(t, c.skipQuiescentGraph(nil), "a sampled zero stamp is an empty graph")
	})
}

// TestBM25Arm_CursorHeldOnShipFailure asserts the ordering that makes a transient
// failure survivable: ship, then advance — never the other way.
func TestBM25Arm_CursorHeldOnShipFailure(t *testing.T) {
	ctx := context.Background()
	gt, name := kgtypes.GraphCode, "repo"

	fsm := &fakeShipManager{fieldsErr: errors.New("seal boom")}
	start := []*knowledgev1.LayerCursor{{LayerKey: "default", AfterUpdatedAt: 100, AfterId: "a"}}
	require.NoError(t, fsm.SaveBM25Cursors(gt, name, start))
	savesBefore := fsm.cursorSaves

	c := bm25TestCollector(fsm, gt, name, func() (int64, bool) { return 9_999, true })
	scanner := &fakeCorpusScanner{pages: []*knowledgev1.CorpusDeltaResponse{bm25Page([]string{"n1"}, nil)}}

	_, err := c.drainBM25(ctx, scanner, start)
	require.Error(t, err, "a failed ship must surface, not be swallowed")

	held, lerr := fsm.LoadBM25Cursors(gt, name)
	require.NoError(t, lerr)
	assert.Equal(t, int64(100), held[0].GetAfterUpdatedAt(),
		"the persisted cursor must be UNCHANGED — advancing past an unshipped page converts a "+
			"transient failure into permanently unindexed nodes")
	assert.Equal(t, savesBefore, fsm.cursorSaves, "and no save was even attempted")

	t.Run("the_next_tick_re_drains_and_re_ships_the_same_page", func(t *testing.T) {
		// THE RECOVERY IS ASSERTED, not just the non-advance: a cursor that never
		// advanced at all would satisfy the non-advance alone.
		fsm.fieldsErr = nil
		retry := &fakeCorpusScanner{pages: []*knowledgev1.CorpusDeltaResponse{bm25Page([]string{"n1"}, nil)}}
		cursors, lerr := fsm.LoadBM25Cursors(gt, name)
		require.NoError(t, lerr)

		shipped, derr := c.drainBM25(ctx, retry, cursors)
		require.NoError(t, derr)
		assert.Equal(t, 1, shipped, "the same page is re-shipped")

		advanced, lerr := fsm.LoadBM25Cursors(gt, name)
		require.NoError(t, lerr)
		assert.Greater(t, advanced[0].GetAfterUpdatedAt(), int64(100),
			"and NOW the cursor advances, which is what makes the hold a retry rather than a stall")
	})
}

// TestBM25Arm_TombstonesGoToTheDeleteSeam pins the page split. A tombstoned row
// carries no composed entry, and a live row that composed to nothing also carries
// none — so splitting on the absent entry rather than on tombstoned_at would delete
// live rows that simply had no indexable text.
func TestBM25Arm_TombstonesGoToTheDeleteSeam(t *testing.T) {
	page := bm25Page([]string{"live-1"}, []string{"dead-1"})
	// A live row with NO composed entry — the case that must not be read as a delete.
	page.Items = append(page.Items, &knowledgev1.Node{Id: "live-no-text", UpdatedAt: 5_000_000})

	docs, deleted := partitionBM25Page(page)

	assert.Equal(t, []searchengine.ExternalID{"dead-1"}, deleted,
		"only the tombstoned row is a delete")
	require.Len(t, docs, 1, "the text-bearing live row becomes a document")
	assert.Equal(t, "live-1", docs[0].ID)
	assert.NotContains(t, deleted, searchengine.ExternalID("live-no-text"),
		"a live row that composed to nothing is NOT a delete — it simply has no indexable text")
}

// bm25LoopClient satisfies BOTH the collector's WireClient field and the arm's
// CorpusDeltaScanner assertion, which is what lets a test drive runBM25Loop END TO
// END rather than calling its halves by hand.
//
// IT EXISTS BECAUSE THE OTHER QUIESCENCE TESTS FLANK THE SEAM WITHOUT CROSSING IT.
// TestBM25Arm_QuiescentGraphWalksZeroTimes calls skipQuiescentGraph and drainBM25
// directly, re-implementing the loop's branch in the test body;
// TestBM25Arm_QuiescenceFlushFiresOnBM25Drain calls maybeBM25Flush directly. Both
// pass with the loop wired ANY way at all, so neither can see whether the loop
// hands over between them — which is exactly the property the test below pins.
type bm25LoopClient struct{ scanner *fakeCorpusScanner }

func (c *bm25LoopClient) PipelineGenPoll(
	context.Context, *knowledgev1.PipelineGenPollRequest,
) (*knowledgev1.PipelineGenPollResponse, error) {
	return &knowledgev1.PipelineGenPollResponse{}, nil
}

func (c *bm25LoopClient) PipelineScan(
	context.Context, *knowledgev1.PipelineScanRequest,
) (*knowledgev1.PipelineScanResponse, error) {
	return &knowledgev1.PipelineScanResponse{}, nil
}

func (c *bm25LoopClient) Execute(
	context.Context, *knowledgev1.ExecuteRequest,
) (*knowledgev1.ExecuteResponse, error) {
	return &knowledgev1.ExecuteResponse{}, nil
}

func (c *bm25LoopClient) CorpusDelta(
	ctx context.Context, req *knowledgev1.CorpusDeltaRequest,
) (*knowledgev1.CorpusDeltaResponse, error) {
	return c.scanner.CorpusDelta(ctx, req)
}

// TestBM25Arm_QuiescentTickStillEvaluatesTheFlushEdge pins the hand-over inside
// runBM25Loop: the flush edge is evaluated on EVERY tick, including a tick the
// change gate held, so a drain that ended on a previous tick still seals its tail.
//
// WITHOUT THIS THE PROPERTY IS UNPROTECTED. A drain arms the latch and returns; if
// the flush call sat inside the drain branch instead of below it, the sealing tick
// would never come and a sub-threshold tail would stay unsealed until the graph
// happened to change again. Verified to fail-when-absent by moving the flush call
// inside the branch: this test times out on its flush signal while every other
// test in the package still passes.
func TestBM25Arm_QuiescentTickStillEvaluatesTheFlushEdge(t *testing.T) {
	// bm25Page advances NextCursors to this value, so a stamp EQUAL to it means the
	// gate opens for tick 1 and holds on every tick after the drain.
	const stamp = 1_000_000

	fsm := &fakeShipManager{}
	c := bm25TestCollector(fsm, kgtypes.GraphCode, "repo",
		func() (int64, bool) { return stamp, true })
	scanner := &fakeCorpusScanner{
		pages: []*knowledgev1.CorpusDeltaResponse{bm25Page([]string{"n1"}, nil)},
	}
	c.client = &bm25LoopClient{scanner: scanner}
	c.baseTick = time.Millisecond
	c.idleTick = time.Millisecond

	// The flush signals through a CHANNEL rather than a counter the test polls:
	// every fakeShipManager write happens on the loop goroutine, so the test reads
	// that struct only after the goroutine has exited.
	flushed := make(chan struct{}, 1)
	c.flush = func(context.Context) error {
		select {
		case flushed <- struct{}{}:
		default:
		}
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); c.runBM25Loop(ctx) }()

	select {
	case <-flushed:
	case <-time.After(5 * time.Second):
		cancel()
		<-done
		t.Fatal("a quiescent tick never evaluated the flush edge: tick 1 drained and armed the " +
			"latch, and no later tick sealed its tail")
	}
	cancel()
	<-done

	assert.Equal(t, 1, scanner.calls,
		"KNOWN POSITIVE: exactly one walk was paid, so the gate genuinely HELD after tick 1 — "+
			"which is what makes the flush above a quiescent-tick flush rather than a second drain's")
}

// TestBM25Arm_QuiescenceFlushFiresOnBM25Drain covers the case the embed-axis flush
// structurally cannot: a graph with ZERO embed work still has to seal its
// sub-threshold BM25 tail.
func TestBM25Arm_QuiescenceFlushFiresOnBM25Drain(t *testing.T) {
	ctx := context.Background()
	fsm := &fakeShipManager{}
	c := bm25TestCollector(fsm, kgtypes.GraphCode, "repo", func() (int64, bool) { return 0, true })

	pending := false
	// Two ticks carrying rows — the latch arms and must NOT flush yet.
	pending = c.maybeBM25Flush(ctx, 3, pending)
	pending = c.maybeBM25Flush(ctx, 2, pending)
	require.Zero(t, fsm.flushCalls, "a drain still moving rows must not seal mid-flight")

	// The drain-complete edge.
	pending = c.maybeBM25Flush(ctx, 0, pending)
	require.Equal(t, 1, fsm.flushCalls, "the flush fires exactly once on the drain-complete edge")
	require.Equal(t, []graphKey{{GraphType: kgtypes.GraphCode, GraphName: "repo"}}, fsm.flushKeys,
		"and is scoped to this arm's own graph")

	// Post-drain idle ticks must not re-fire it.
	for range 4 {
		pending = c.maybeBM25Flush(ctx, 0, pending)
	}
	assert.Equal(t, 1, fsm.flushCalls,
		"the latch is what makes this per-DRAIN rather than per-tick; without it every idle "+
			"tick would re-seal")
	assert.False(t, pending)
}
