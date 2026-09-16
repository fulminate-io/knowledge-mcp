// SPDX-License-Identifier: Apache-2.0

// pipeline_bm25_only.go — the BM25-ONLY wiring arm the three gates in
// wirePipelineRuntime hand off to instead of returning with nothing wired.
//
// Split out of pipeline.go for the 500-line cap that file already sits near, and
// kept whole in one place so the SET of things a BM25-only daemon runs is read
// off one function rather than assembled from three call sites.

package bootstrap

import (
	"context"
	"log/slog"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/pipeline"
)

// wireBM25OnlyRuntime constructs the client pipeline with NO summarizer and NO
// embedder, so the only thing that comes alive is the deterministic BM25 arm and
// the one loop that arm cannot run without. It is what the three gates in
// wirePipelineRuntime call instead of returning nil, and each of them logs its
// own reason before calling it — narrowing a gate must never turn it silent.
//
// WHY THE ARM MUST NOT BE GATED ON AN LLM AXIS AT ALL. The BM25 arm is
// deterministic: it reads the server's CorpusDelta pages and seals them into the
// local BM25 pool. It needs no summarizer, no embedder and no vector, and its own
// enable predicate says so — bm25ArmEnabledFor is `hasManager &&
// HasRebuildableSegments(gt)` (pipeline/collector_bm25.go). It was nevertheless
// unreachable on a keyless or --headless daemon for one structural reason: its
// closures are built inside RegisterGraph, which lives inside the pipeline, which
// these three gates skipped whole. A client with no embed credential therefore
// served ZERO results for text search for the life of the process, which is the
// defect the keyless-BM25 fix repairs.
//
// THE TWO LLM AXES STAY OFF, and they stay off BY CONSTRUCTION rather than by a
// flag: Pipeline.Start spawns a dispatcher and a worker pool per axis only when
// that axis's function is non-nil (pipeline/pipeline.go), and collector.run gates
// each per-collector loop the same way, so a nil/nil pipeline spawns no summary
// and no embed work anywhere. --headless keeps meaning "no LLM work".
//
// WHAT RUNS, AND WHY EACH ONE IS REQUIRED RATHER THAN CONVENIENT:
//
//   - Start: spawns nothing here (both axes nil) but owns the WaitGroups Stop
//     waits on, so the lifecycle stays the one Stop understands.
//   - RefreshOnceForBoot + RefreshLoadedGraphs: the working-set diff that
//     REGISTERS a collector. Without the loop a keyless daemon registers nothing
//     after boot, because on a cold boot the working set is empty and the first
//     admission arrives later — from a search.
//   - RunGenPollLoop: the arm's change gate compares the server's corpus stamp
//     against its own durable cursor, and that stamp comes off THIS loop's shared
//     snapshot (corpusStampFor). The arm cannot decide anything without it. It is
//     new RPC traffic on a --headless daemon; that cost is real and is stated
//     here rather than hidden.
//
// WHAT STAYS SKIPPED, each for a reason and not for tidiness. This is the
// orchestrator's ruling inside the settlement that --headless "keeps its two LLM
// axes and stops gating the deterministic BM25 arm": --headless keeps skipping
// every background coordination loop EXCEPT the minimum the arm needs.
//
//   - attachSegmentDependentHooks, which carries all three of:
//     AttachHealFactory — it authorizes exactly the rebuild that is vector-gated
//     server-side, so on a keyless graph it would fire, be refused, and re-arm in
//     a loop. Removing it by construction beats relying on the refusal being
//     harmless.
//     attachBalanceVerdict — a quiescence-edge diagnostic the arm does not need.
//     SetStorageSegmentNudger — it nudges the reconcile loop, which is itself
//     skipped below, so wiring it would poke nothing.
//   - spawnBootSegmentPasses — a boot-time heal-and-report pass is precisely the
//     eager startup work --headless exists to suppress, and "convergence is
//     always lazy" argues the same way.
//   - runSegmentReconcileLoop — not required by the arm. It is also the loop that
//     would reconcile an operator's existing ROOT pool into the destination pool,
//     so a BM25-only daemon does not reconcile. Stated here rather than
//     discovered.
//   - the summarizer-chain health prober — there is no chain.
//
// The attachments that DO ride along (the segment manager, the working set, the
// storage owners, the graph evictor, the local-presence predicate and the collect
// gate) are wirings rather than loops, and each is inert without the runtime it
// names. attachLocalPresence in particular MOVES WITH THE CONSTRUCTION wherever
// the construction goes: nothing can observe its wiring from a test, and an
// unwired presence predicate is PERMISSIVE.
func wireBM25OnlyRuntime(ctx context.Context, c *client, f Config) error {
	// bootCtx backs the boot-synchronous Start for the same reason the full wiring
	// does: a shutdown mid-boot must not abort the Start lifecycle that
	// pipeline.Stop owns, because binding Start to c.wireCtx would double-cancel
	// the WaitGroups Stop already owns. The long-lived loops below take the passed
	// ctx, which drainOnShutdown cancels.
	//
	// WithoutCancel RATHER THAN A BARE Background(): the detached lifetime is the
	// deliberate decision here, and this spelling says so while still forwarding
	// the caller's values. A bare Background() reads as an oversight — it is the
	// shape that silently severs a caller's deadline — and the corpus check for
	// that class is right to flag it even where the detachment is wanted.
	bootCtx := context.WithoutCancel(ctx)

	p := pipeline.New(bm25OnlyPipelineConfig(f), routedWireClient{router: c.router}, nil, nil)
	p.AttachSegmentManager(c.segmentMgr)
	p.AttachWorkingSet(c.workingSet)
	wireStoragePipelineOwners(c, p)
	p.AttachStorageGraphEvictor(func(ectx context.Context, gt kgtypes.GraphType, name, reason string) {
		if c.RemoveStorageFromWorkingSet(ectx, gt, name) {
			slog.Info("working set: graph evicted",
				"graph_type", gt, "name", name, "reason", reason)
		}
	})
	attachLocalPresence(p, c)
	attachCollectGate(p, c)

	c.pipeline = p
	// Installed together with the pipeline, exactly as the full wiring installs
	// them, so no half-wired state exists for fuseCaughtUp to distinguish.
	c.serverSegmentStamp = p.SegmentStampFor
	if err := p.Start(bootCtx); err != nil {
		return err
	}
	p.RefreshOnceForBoot(bootCtx) //nolint:errcheck // best-effort initial seed
	go p.RefreshLoadedGraphs(ctx)
	go p.RunGenPollLoop(ctx)

	slog.Info("client pipeline: BM25-only arm wired (no summarizer, no embedder)",
		"summary_axis", "off", "embed_axis", "off",
		"loops", "collector-bm25 + gen-poll",
		"skipped", "heal-factory, balance-verdict, segment-nudger, boot-segment-passes, segment-reconcile")
	return nil
}

// bm25OnlyPipelineConfig is the pipeline Config a BM25-only client runs under. It
// carries the tick and NOTHING about the two LLM axes, deliberately: with both
// functions nil no dispatcher, worker pool, lease or provider label is ever read,
// and copying the channel sizes and provider names across would suggest an axis
// that does not exist here.
func bm25OnlyPipelineConfig(f Config) pipeline.Config {
	return pipeline.Config{Tick: f.PipelineTick}
}
