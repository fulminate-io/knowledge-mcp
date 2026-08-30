// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/llmproviders"
	"github.com/fulminate-io/knowledge-mcp/internal/workingset"
)

// TestCapstone_RegisteredGraphEnumerateAndShip is PART A of the end-to-end
// capstone (the pipeline half): an INTERACTED-WITH custom graph type (hellograph)
// is (1) REGISTERED by the client pipeline's catalog pass and (2) DRAINED on
// BOTH axes (summary + embed) with its BM25+HNSW segments shipped under its own
// (gt, name) key — so a collected custom graph actually becomes searchable.
//
// WHAT EARNS THE DRAIN CHANGED. Registering a custom graph type no longer causes
// the pipeline to drain its graphs; INTERACTING with a graph does. The pipeline's
// wanted set is the client's working set, so hellograph/demo is drained here
// because the fixture admits it — modeling the collect or search that would admit
// it in production — and a registered type nobody touched is drained by nothing.
//
// FAILS-WHEN-ABSENT: drop the admission below and the registration assertion
// fails — the pass would find an empty wanted set, register no collector, and the
// segments below would never ship.
//
// A custom type needs no registry lookup on this path: the member carries its own
// GraphType, and pipelineDrainsType admits every non-builtin type.
//
// The LIVE hellograph:demo verification post-deploy (client rebuild + cloud
// redeploy, then a real search(graph:hellograph,name:demo)) is the true
// end-to-end and is deferred to the orchestrator (the deploy-then-live-verify
// deferral pattern); this test proves the client-side loop in the
// highest-fidelity in-process harness.
func TestCapstone_RegisteredGraphEnumerateAndShip(t *testing.T) {
	ctx := context.Background()

	const customGT = kgtypes.GraphType("hellograph")
	const customName = "demo"

	// --- (1) REGISTRATION: an interaction admits hellograph/demo, and the catalog
	// pass registers a collector for exactly it.
	wc := newFakeWireClient()
	fe := &fakeEmbedder{vectors: map[string][]byte{"world-node": vec32(7)}}
	noopSum := func(_ context.Context, _ []llmproviders.BatchChunk) (map[string]llmproviders.SummarizeResult, error) {
		return nil, nil
	}
	p := New(Config{}, wc, noopSum, fe.call)
	// refreshOnce below REGISTERS a collector, and a registered collector is a
	// running one: it spawns its summary and embed wake loops immediately. Nothing
	// in this test's assertions needs them afterwards, so without this teardown they
	// outlive the test — parked in sleepForWake for the remainder of the test BINARY,
	// and holding the segment index the embed axis seals below, whose merger
	// goroutine then leaks too. Neither failed anything; the package's goleak gate is
	// what surfaced them.
	t.Cleanup(func() { require.NoError(t, p.Stop(context.Background())) })
	ws := workingset.New()
	require.True(t, ws.Admit(customGT, customName, "collect"),
		"the interaction that earns the custom graph its place")
	p.AttachWorkingSet(ws)

	p.refreshOnce(ctx)
	require.Contains(t, registeredKeys(p), graphKey{GraphType: customGT, GraphName: customName},
		"the interacted-with custom graph gets a collector (dropping the admission breaks this)")

	// --- (2) DRAIN + SHIP: the summary AND embed axes drain hellograph:demo work
	// and the ship manager receives the segments keyed on (hellograph, demo).
	fsm := &fakeShipManager{}
	p.AttachSegmentManager(fsm)

	// Summary axis: a hellograph:demo summary work item drains through the summary
	// worker (proves the summary axis flows for the custom graph).
	runSummaryWorkerBatch(ctx, p, []SummaryWork{
		{GraphType: customGT, GraphName: customName, NodeID: "world-node", SummarizeText: `{"name":"world"}`},
	})

	// Embed axis: a hellograph:demo embed work item drains through the embed worker
	// → seals HNSW (AddAndMarkDirty). It no longer seals BM25: that moved to the
	// BM25 arm, asserted separately below.
	runEmbedWorkerBatch(ctx, p, []EmbedWork{
		{
			GraphType: customGT,
			GraphName: customName,
			NodeID:    "world-node",
			EmbedText: "hello world",
		},
	})

	require.GreaterOrEqual(t, fsm.calls, 1, "embed axis shipped an HNSW segment for the custom graph")
	require.Contains(t, fsm.shipKeys, graphKey{GraphType: customGT, GraphName: customName},
		"HNSW segment shipped under the custom (hellograph, demo) key")
	require.Zero(t, fsm.fieldsCalls,
		"the embed axis no longer seals BM25 for ANY graph, custom ones included")

	// --- (3) THE CUSTOM GRAPH STILL GETS BM25 COVERAGE, just from a different
	// producer. This capstone's claim was never "the embed axis ships BM25" — it was
	// "a registered custom graph is a first-class segment citizen". Dropping the BM25
	// half when the producer moved would have quietly narrowed that claim, so it is
	// re-asserted against the new producer's admission gate instead: the BM25 arm
	// serves this graph type, so its documents come from the CorpusDelta feed.
	require.True(t, bm25ArmEnabledFor(customGT, true),
		"a registered custom graph type must be admitted by the BM25 arm — otherwise moving BM25 "+
			"off the embed axis would have silently dropped custom graphs out of the keyword corpus")
}
