// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"testing"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/config"
	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/pipeline"
	"github.com/fulminate-io/knowledge-mcp/internal/workingset"
)

// TestNoLLMPipelineStillWiresTheBM25Arm is the in-process half of the keyless-BM25 fix's headless row:
// a client launched with --no-llm-pipeline (what --headless expands to) must come
// out of wirePipelineRuntime with a pipeline attached, because that pipeline is
// where the deterministic BM25 arm's closures are built.
//
// RED ON THE RELEASE THE DEFECT WAS REPRODUCED ON: the gate returned nil with nothing wired, so c.pipeline and
// c.serverSegmentStamp were both nil and no BM25 document was ever produced —
// text search returned 0 results for the life of the process.
//
// THE LOOP-SET HALF IS NOT HERE, and that is a property of the code rather than
// a gap: the heal factory, the balance verdict, the segment nudger and the two
// segment loops are unexported wirings inside package pipeline with no exported
// reader, so nothing in this process can observe their absence. The daemon's own
// log can, which is why that assertion is the spawned row
// (TestKeylessHeadlessDaemon_* in cmd/server-bench/internal/bench) and why it
// carries a same-run known positive.
func TestNoLLMPipelineStillWiresTheBM25Arm(t *testing.T) {
	selectAccountForTest(t, "")
	c := newPoolIdentityClient(t)
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	if c.pipeline != nil {
		t.Fatal("fixture check: the client must start with no pipeline, else this row proves nothing")
	}
	if err := wirePipelineRuntime(ctx, c, Config{NoLLMPipeline: true, PipelineTick: time.Hour}); err != nil {
		t.Fatalf("wirePipelineRuntime(--no-llm-pipeline) returned error: %v", err)
	}
	// THE NIL GUARD IS NOT DEFENSIVE PADDING: on the UNFIXED tree this wiring
	// attaches no pipeline at all, which is the red below, and an unguarded Stop
	// would panic in cleanup and bury that red under a stack trace.
	t.Cleanup(func() { stopPipeline(c) })

	if c.pipeline == nil {
		t.Fatal("wirePipelineRuntime(--no-llm-pipeline) attached NO pipeline — the BM25 arm's closures are " +
			"built inside RegisterGraph, so a client with no pipeline produces no BM25 documents at all and " +
			"text search returns nothing for the life of the process")
	}
	if c.serverSegmentStamp == nil {
		t.Error("the server segment-stamp reader was not installed — it is wired from the SAME pipeline so " +
			"the two are installed together or not at all")
	}

	// AND THE ARM REACHES A REGISTRATION: a search admits the graph through the
	// production admitter, and the pipeline's own refresh pass registers it. This is
	// the whole producer chain a keyless daemon depends on, driven end to end in
	// process.
	searchCtx := graphclient.WithDestination(ctx, graphclient.Destination{Storage: "local"})
	if _, err := c.segmentMgr.Search(searchCtx, kgtypes.GraphKnowledge, "default", "anything", nil, 5); err != nil {
		t.Fatalf("Manager.Search returned error: %v", err)
	}
	// HasRef, not Has: a search now records the DESTINATION it was bound to, and
	// Has() normalizes to a destination-less Ref. That is the same shape the Router
	// and collect admission routes have recorded since #164.
	if !c.WorkingSet().HasRef(workingset.Ref{GraphType: kgtypes.GraphKnowledge, Name: "default", Storage: "local"}) {
		t.Fatalf("the search did not admit {knowledge/default, local} through the production admitter; members=%+v",
			c.WorkingSet().Members())
	}
	c.pipeline.RefreshOnceForBoot(ctx)

	// IT STOPS WITHOUT CANCELING THE WIRING ctx FIRST, DELIBERATELY, and that is a
	// second claim rather than an oversight. wireBM25OnlyRuntime starts
	// RefreshLoadedGraphs under this ctx and that loop is woken by the admission
	// above, so with the ctx still live it can call RegisterGraph while Stop is
	// tearing the registry down. Until stopSequence latched the registry closed,
	// that enlisted a collector Stop could no longer cancel and Stop hung — measured
	// here as a 60s timeout at roughly one run in eight, and on the repository's own
	// push gate. Stopping in this order is now SAFE, and this row is where that is
	// observed from a caller: the sibling below keeps the production order, so the
	// two together say the contract holds either way.
	stopPipelineOrFail(t, c.pipeline)
}

// TestNoSummarizerNoEmbedderStillWiresTheBM25Arm is the keyless-BM25 fix's keyless-config row at the third
// gate: a client with neither LLM axis configured — the keyless install — keeps
// the text-index arm. The summarizer-chain-error gate is the FIRST exit such a
// client takes in production and is covered by the spawned row; this one pins the
// sum==nil && emb==nil exit — the third and last of the three.
//
// RED ON THE RELEASE THE DEFECT WAS REPRODUCED ON: `no summarizer or embedder configured; skipping pipeline
// wire` and a nil pipeline.
func TestNoSummarizerNoEmbedderStillWiresTheBM25Arm(t *testing.T) {
	selectAccountForTest(t, "")
	c := newPoolIdentityClient(t)
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	// THE THIRD GATE IS REACHED THROUGH wirePipelineRuntime ITSELF, not through the
	// arm it hands off to, so this row observes the GATE rather than the handoff.
	// An unloaded config is what makes the summarizer chain build cleanly and
	// return nothing (BuildSummarizerWithFallback: "an unloaded config returns
	// (nil, nil) — the caller disables summarization"), and with no embedder either
	// the exit taken is sum == nil && emb == nil — the keyless install's own state.
	t.Cleanup(config.SetForTest(nil))
	if err := wirePipelineRuntime(ctx, c, Config{PipelineTick: time.Hour}); err != nil {
		t.Fatalf("wirePipelineRuntime(no summarizer, no embedder) returned error: %v", err)
	}
	// THE NIL GUARD IS NOT DEFENSIVE PADDING: on the UNFIXED tree this wiring
	// attaches no pipeline at all, which is the red below, and an unguarded Stop
	// would panic in cleanup and bury that red under a stack trace.
	t.Cleanup(func() { stopPipeline(c) })
	if c.pipeline == nil {
		t.Fatal("wirePipelineRuntime(no summarizer, no embedder) attached NO pipeline — the keyless install " +
			"is the configuration whose ONE indexing path costs no LLM call, so it is the one that most " +
			"needs the deterministic BM25 arm")
	}
	// THE TWO LLM AXES MUST BE OFF. PipelineStatus reads both circuit breakers and
	// is the one exported window onto the axes; neither may be paused, and neither
	// may report an active summarizer, because neither exists.
	if st := c.pipeline.PipelineStatus(); st.Paused || st.Summary.ActiveSummarizer != "" {
		t.Errorf("PipelineStatus() = {Paused:%t ActiveSummarizer:%q}, want an unpaused pipeline with no "+
			"summarizer — a BM25-only client wires neither LLM axis", st.Paused, st.Summary.ActiveSummarizer)
	}
	// THE PRODUCTION SHUTDOWN ORDER: drainOnShutdown cancels c.wireCtx and THEN
	// calls pipeline.Stop. Its sibling above stops without canceling first, so the
	// pair covers both orders — which is the point of latching the registry closed
	// inside Stop rather than relying on callers to cancel in the right order.
	cancel()
	stopPipelineOrFail(t, c.pipeline)
}

// pipelineStopBudget bounds Pipeline.Stop in these tests' cleanups.
//
// IT IS GENEROUS ON PURPOSE. Stop cancels every collector and then WAITS on their
// WaitGroup bounded by the ctx it is given; on a timeout it returns the ctx error
// and the waiter goroutine it spawned stays blocked on wg.Wait forever, which the
// package's suite-level goleak gate reports at the END of the suite rather than
// against the test that caused it. These tests drive a real collector against an
// unreachable backend, so its drain fails and it sits in an error backoff — a
// cancel ends that sleep at once, but only if the wait outlives the scheduling. A
// budget that is merely "enough" turns a scheduling hiccup into a suite-level leak
// report pointing at the wrong test.
const pipelineStopBudget = 60 * time.Second

// stopPipeline stops a client's pipeline when it has one. See the cleanup call
// sites for why the nil arm exists.
func stopPipeline(c *client) {
	if c.pipeline == nil {
		return
	}
	stopCtx, cancel := context.WithTimeout(context.Background(), pipelineStopBudget)
	defer cancel()
	_ = c.pipeline.Stop(stopCtx)
}

// stopPipelineOrFail stops a pipeline and FAILS THE TEST when Stop does not
// finish inside the budget, naming the pipeline that did not unwind.
//
// IT EXISTS SO A STUCK COLLECTOR IS ATTRIBUTABLE. Stop cancels every collector and
// then waits on their WaitGroup bounded by the ctx; on a timeout it returns the
// ctx error AND leaves the waiter goroutine it spawned blocked on wg.Wait forever.
// This package's suite-level goleak gate then reports that goroutine at the END of
// the suite, pointing at no test in particular — which is exactly how a real stuck
// collector reaches a reviewer as an unreproducible suite failure. Checking Stop's
// return HERE turns the same condition into a red on the test that caused it.
//
// EVERY TEST THAT BUILDS A PIPELINE CALLS THIS, and the plain stopPipeline cleanup
// stays as the backstop: Stop is stopOnce, so the second call is free.
func stopPipelineOrFail(t *testing.T, p *pipeline.Pipeline) {
	t.Helper()
	stopCtx, cancel := context.WithTimeout(context.Background(), pipelineStopBudget)
	defer cancel()
	if err := p.Stop(stopCtx); err != nil {
		t.Errorf("Pipeline.Stop did not finish within %s: %v — a collector did not observe its cancel, "+
			"and the waiter Stop spawned is now blocked on wg.Wait for the life of the process",
			pipelineStopBudget, err)
	}
}
