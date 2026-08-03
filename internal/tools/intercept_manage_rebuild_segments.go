// SPDX-License-Identifier: Apache-2.0

// intercept_manage_rebuild_segments.go — client-side manage(rebuild_segments)
// intercept. rebuild_segments BACKFILLS a graph's BM25+HNSW search segments
// from nodes that are ALREADY embedded but have ZERO shipped segments (embedded
// before the segment-ship path existed, or after a SegmentStore prune) — WITHOUT
// re-embedding. It serves the builtin code and knowledge graphs AND any
// registered custom graph type (the manual op registry-gates the target); the
// bootstrap auto-heal closure that shares this core is scoped to code + knowledge
// by its own gate. Unlike rebuild_cache (which lowers to one server-side Index RPC),
// the WORK is CLIENT-driven: the server is engine-free, so the client pages the
// new segment_rebuild PipelineScan axis (already-embedded nodes WITH their stored
// vector + server-composed BM25 fields), rebuilds the segments DETERMINISTICALLY,
// and ships them to the server SegmentStore.
//
// Determinism + idempotency: the build uses the deterministic HNSW path (fixed
// seed + serial-within-segment + concurrent-across-segments), so a re-run over an
// unchanged node set produces byte-identical segments → identical content hash →
// the ship diff is empty → a true no-op. Segment membership is decided by HASHING
// each node's id into a bucket, so it depends on the NODE rather than on where the
// node fell in the scan: adding or removing one node re-emits the single bucket it
// belongs to, leaving every other bucket byte-identical. The FIRST rebuild over an
// embed-segmented graph ships the deterministic segments and reconcilePrune drops
// the superseded embed ones server-side; the superseded local .seg files are then
// evicted via InvalidateLocal so they do not orphan under an unbounded cache.
//
// The actual scan/build/ship work — and the per-graphKey single-flight guard —
// live in the reusable RebuildSegments core, which is ALSO invoked by the
// auto-heal closure (built in bootstrap over the segment manager). Both
// callers coalesce onto one run via the shared guard; handleClientRebuildSegments
// is now a thin validate-invoke-render wrapper over that core.

package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// handleClientRebuildSegments drives the client-side segment backfill for one
// graph (the builtin code or knowledge graph, or a registered custom graph type).
// It runs SYNCHRONOUSLY (rendering the shipped/ pruned counts) but is single-flight
// per (graph,name). Flow:
//  1. registry-gate the graph (builtin code/knowledge OR a registered custom graph
//     type; reject empty / other builtin / unregistered typo; default the knowledge
//     name to "default" and reject a knowledge overlay name) + non-empty name;
//     resolve the PipelineScanner + SegmentShipper deps (error "pipeline not
//     wired" on nil).
//  2. load the persisted watermark (zeroed by reset) and page the segment_rebuild
//     scan from it by the stable after_id id-cursor; terminate on an EMPTY page
//     (the set is stable, so a full final page is normal).
//  3. accumulate items id-ascending; group them by hash bucket; build each group's
//     HNSW + BM25 Documents CONCURRENTLY (NumCPU pool), then STAGE each group serially
//     through StageRebuildPartition, which carries both formats in one call and writes
//     to no engine — NO per-group ship (the concurrent-ship/reconcilePrune race fix).
//  4. if any staging call errored, ABORT before the finalize (nothing is shipped).
//  5. otherwise FinalizeRebuild ONCE (ships, reconciles, returns the pruned
//     ids and whether the manifest swap landed), then InvalidateLocal evicts the
//     superseded local .seg files and — only on a landed swap — the watermark
//     advances to the horizon the server served.
func handleClientRebuildSegments(ctx context.Context, deps ClientDeps, a manageArgs) kgtools.ToolResult {
	// Graph gate, registry-aware (segmentTarget shape): accept the builtin `code`
	// or `knowledge` graph OR any registered custom graph type; reject an empty
	// graph, any other builtin (practice/cloud/cicd carry no rebuildable segments),
	// and an unregistered typo.
	switch {
	case a.Graph == "":
		return errorResult(`manage(rebuild_segments) requires "graph" — name the code, knowledge, or registered custom graph to rebuild`)
	case kgtypes.IsBuiltinGraphType(a.Graph):
		// A builtin carries rebuildable segments iff its graph type is embeddable.
		// Gate on HasRebuildableSegments — the client mirror of the SAME Embeddable()
		// predicate the server's segment_rebuild scan uses — so the client gate and
		// the server scan cannot drift. This admits every embeddable builtin (code,
		// knowledge, cloud, cicd, practice) and still rejects the non-embeddable ones
		// (linkage, transformers, logs, web, pdf), which have no segments to rebuild.
		if !kgtypes.HasRebuildableSegments(kgtypes.GraphType(a.Graph)) {
			return errorResult(fmt.Sprintf(`manage(rebuild_segments): builtin graph %q has no rebuildable segments — only embeddable graphs (code, knowledge, cloud, cicd, practice) and registered custom graph types are supported`, a.Graph))
		}
		// The builtin knowledge graph has one canonical instance named "default";
		// an empty name (or the "knowledge" alias) resolves to it. BASE layer only
		// in v1 — an "@"-suffixed overlay/session name is rejected (no overlay
		// segment rebuilds yet).
		if a.Graph == string(kgtypes.GraphKnowledge) {
			if strings.ContainsRune(a.Name, '@') {
				return errorResult(fmt.Sprintf(`manage(rebuild_segments): knowledge overlay name %q is not supported — overlay rebuilds not supported in v1 (base "default" layer only)`, a.Name))
			}
			if a.Name == "" || a.Name == string(kgtypes.GraphKnowledge) {
				a.Name = "default"
			}
		}
	default:
		// Non-builtin: accept only when a GraphTypeDef record resolves (registered
		// custom type), mirroring collect.go:192. A nil registry (degraded client)
		// cannot confirm registration, so the typo error stands.
		crud := deps.GraphTypeCRUD()
		if crud == nil {
			return errorResult(fmt.Sprintf(`manage(rebuild_segments): graph %q is not a registered custom graph type (registry unavailable)`, a.Graph))
		}
		if _, found, _ := crud.ByName(ctx, a.Graph); !found {
			return errorResult(fmt.Sprintf(`manage(rebuild_segments): graph %q is not code and not a registered custom graph type`, a.Graph))
		}
	}
	if a.Name == "" {
		return errorResult(`manage(rebuild_segments) requires "name" — the repo whose segments to rebuild`)
	}

	// Readiness gate (bind-first startup): rebuild_segments needs the pipeline-backed scanner
	// + shipper, which are nil during the bind-first wiring window. Distinguish the
	// transient window from a permanently degraded (no-pipeline) client so a retry
	// succeeds.
	if !deps.PipelineReady() {
		return errorResult("manage(rebuild_segments): daemon still starting — LLM pipeline not ready yet, retry shortly")
	}
	scanner := deps.PipelineScanner()
	shipper := deps.SegmentShipper()
	if scanner == nil || shipper == nil {
		return errorResult("manage(rebuild_segments): pipeline not wired — the client is running in degraded mode (no segment engine)")
	}

	// The single-flight guard + scan/build/flush all live in the shared core
	// (also called by the auto-heal closure). The wrapper only validates,
	// invokes, and renders.
	out, err := RebuildSegments(ctx, scanner, shipper, kgtypes.GraphType(a.Graph), a.Name, a.Reset)
	if err != nil {
		return errorResult("manage(rebuild_segments): " + err.Error())
	}
	if !out.Ran {
		return textResult(fmt.Sprintf("rebuild_segments already in progress for %s/%s — ignoring the duplicate request", a.Graph, a.Name))
	}
	if out.Scanned == 0 {
		return textResult(fmt.Sprintf("rebuild_segments: %s/%s has no embedded nodes to rebuild segments from — nothing to do", a.Graph, a.Name))
	}

	// Manual-op success (scanned>0): re-arm the auto-heal breaker for this graph. An
	// operator asking for a rebuild that actually scanned nodes clears any latched
	// disarm so the automatic embed-drain / reconcile heal resumes — the deliberate
	// manual→clear→auto-refire re-arm (keyed on scanned>0, NOT built>0: built is
	// routinely 0 on a legit sub-1024 heal).
	deps.ClearHealLatch(kgtypes.GraphType(a.Graph), a.Name)

	// SHIPPED IS NOT PUBLISHED, and the operator-facing text is where that has to be
	// said. A refused publish returns a NIL ERROR — the coverage gate skips a
	// degenerate live set and the agent skips a manifest referencing a blob it has not
	// seen, both quietly — so the counts above describe blobs that landed while the
	// live set never swapped. An unqualified "complete" over that state is how the
	// failed clean restore was scored as having run, and a WARN in the daemon log is
	// not a surface the operator issuing this op ever sees.
	if !out.Published {
		return textResult(fmt.Sprintf(
			"rebuild_segments INCOMPLETE for %s/%s: %d embedded nodes scanned and %d hash buckets built + shipped, but the manifest swap was REFUSED — the rebuilt segments are NOT the live set and the corpus is NOT restored. The publish gate skips a live set that does not cover the shipped corpus, and the daemon log carries the reason. Re-run after the resident pool recovers; nothing was lost, the prior manifest and blobs are intact.",
			a.Graph, a.Name, out.Scanned, out.Built))
	}

	return textResult(fmt.Sprintf(
		"rebuild_segments complete for %s/%s: %d embedded nodes scanned, %d hash buckets built + shipped and PUBLISHED as the live set, %d hnsw + %d bm25 superseded segments pruned (hnsw local cache invalidated). Re-running is a content-hash no-op, and a later run re-emits only the buckets whose members changed.",
		a.Graph, a.Name, out.Scanned, out.Built, len(out.HNSWPruned), len(out.BM25Pruned)))
}
