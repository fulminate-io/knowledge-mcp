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

	// Embed axis: a hellograph:demo embed work item with BM25 fields drains through
	// the embed worker → seals HNSW (AddAndMarkDirty) AND BM25 (AddAndMarkDirtyFields).
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
