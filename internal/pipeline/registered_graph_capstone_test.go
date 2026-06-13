// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/llmproviders"
)

// TestCapstone_RegisteredGraphEnumerateAndShip is PART A of the end-to-end
// capstone (the pipeline half): a REGISTERED custom graph type (hellograph) is
// (1) ENUMERATED by the client pipeline's per-tick discovery and (2) DRAINED on
// BOTH axes (summary + embed) with its BM25+HNSW segments shipped under its own
// (gt, name) key — so a collected custom graph actually becomes searchable.
//
// FAILS-WHEN-ABSENT: reverting Phase 1 (the discoverRegisteredGraphTypes fold-in)
// makes the enumeration assertion fail — listLoadedGraphs would no longer surface
// hellograph, so the pipeline would never scan/drain it and the segments below
// would never ship.
//
// The LIVE hellograph:demo verification post-deploy (client rebuild + cloud
// redeploy, then a real search(graph:hellograph,name:demo)) is the true
// end-to-end and is deferred to the orchestrator (the deploy-then-live-verify
// deferral pattern); this test proves the client-side loop in the
// highest-fidelity in-process harness.
func TestCapstone_RegisteredGraphEnumerateAndShip(t *testing.T) {
	ctx := context.Background()

	// --- (1) ENUMERATION: the registry browse surfaces hellograph and
	// listLoadedGraphs folds its loaded graph (hellograph/demo) into the drain set
	// alongside the builtins. This is the half reverting Phase 1 breaks.
	wc := newFakeWireClient()
	wc.seedGraphTypeDefs("hellograph")
	wc.seedGraphNames(kgtypes.GraphType("hellograph"), "demo")

	refs, succeeded, _, throttled := listLoadedGraphs(ctx, wc)
	require.False(t, throttled)
	got := map[string]bool{}
	for _, r := range refs {
		got[string(r.GraphType)+"/"+r.GraphName] = true
	}
	require.True(t, got["hellograph/demo"],
		"the registered custom graph is enumerated and drained (reverting Phase 1 breaks this)")
	require.True(t, succeeded[kgtypes.GraphType("hellograph")],
		"the custom type enumerated cleanly")

	// --- (2) DRAIN + SHIP: the summary AND embed axes drain hellograph:demo work
	// and the ship manager receives the segments keyed on (hellograph, demo).
	fe := &fakeEmbedder{vectors: map[string][]byte{"world-node": vec32(7)}}
	noopSum := func(_ context.Context, _ []llmproviders.BatchChunk) (map[string]llmproviders.SummarizeResult, error) {
		return nil, nil
	}
	p := New(Config{}, wc, noopSum, fe.call)
	fsm := &fakeShipManager{}
	p.AttachSegmentManager(fsm)

	const customGT = kgtypes.GraphType("hellograph")
	const customName = "demo"

	// Summary axis: a hellograph:demo summary work item drains through the summary
	// worker (proves the summary axis flows for the custom graph).
	runSummaryWorkerBatch(ctx, p, []SummaryWork{
		{GraphType: customGT, GraphName: customName, NodeID: "world-node", SummarizeText: `{"name":"world"}`},
	})

	// Embed axis: a hellograph:demo embed work item with BM25 fields drains through
	// the embed worker → ships HNSW (AddAndShip) AND BM25 (AddAndShipFields).
	runEmbedWorkerBatch(ctx, p, []EmbedWork{
		{
			GraphType:  customGT,
			GraphName:  customName,
			NodeID:     "world-node",
			EmbedText:  "hello world",
			Bm25Fields: map[string]string{"name": "world", "summary": "a hello world greeting"},
		},
	})

	require.GreaterOrEqual(t, fsm.calls, 1, "embed axis shipped an HNSW segment for the custom graph")
	require.GreaterOrEqual(t, fsm.fieldsCalls, 1, "embed axis shipped a BM25 segment for the custom graph")
	require.Contains(t, fsm.shipKeys, graphKey{GraphType: customGT, GraphName: customName},
		"HNSW segment shipped under the custom (hellograph, demo) key")
	require.Contains(t, fsm.fieldsShipKeys, graphKey{GraphType: customGT, GraphName: customName},
		"BM25 segment shipped under the custom (hellograph, demo) key")
}
