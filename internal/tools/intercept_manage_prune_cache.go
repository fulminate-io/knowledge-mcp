// SPDX-License-Identifier: Apache-2.0

// intercept_manage_prune_cache.go — client-side manage(prune-cache) intercept.
// prune-cache is a ONE-SHOT operator backlog reclaim: it diffs each
// segment-bearing graph's on-disk L2 .seg files against that graph's COMPLETE
// current live set (force-full-loaded so unloaded-but-live segments still count;
// HNSW = embed ∪ deterministic; cross-checked against List(0) with a subset-abort)
// and removes the orphans — the accumulated superseded blobs the
// invalidation-driven reclaim never unlinked.
//
// ITS TARGET SET IS THE WORKING SET, not the catalog. The force-full-load is
// required for safety and is exempt from any attempt to make it cheaper, so the
// gate is applied to WHICH graphs reach it rather than to what it does — see the
// argument at the enumeration in handleClientPruneCache.
//
// It PREVIEWS by default (execute=false renders a would-remove report and deletes
// NOTHING); execute=true performs the removal. The data-loss-critical logic lives
// in segmentdist.Manager.PruneCache behind the SegmentPruner seam — this handler
// only gates readiness, enumerates the target graphs, and renders the report.

package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// handleClientPruneCache drives the one-shot prune-cache op. It rejects a
// not-ready / degraded client, enumerates the two segment-bearing builtins
// (knowledge/default + every code repo) into the PARALLEL (graphTypes, names)
// slices the SegmentPruner seam takes, calls PruneCache with the Execute gate, and
// renders a would-remove preview (Execute=false) or a removed-N result
// (Execute=true) — surfacing any List(0)-aborted pools. format=json returns the
// structured report verbatim.
func handleClientPruneCache(ctx context.Context, deps ClientDeps, a manageArgs) kgtools.ToolResult {
	// Readiness gate (bind-first startup): the segment manager is constructed in the
	// background wiring window. Distinguish the transient window from a permanently
	// degraded (no-segment-engine) client so a retry succeeds.
	if !deps.PipelineReady() {
		return errorResult("manage(prune-cache): daemon still starting — segment engine not ready yet, retry shortly")
	}
	pruner := deps.SegmentPruner()
	if pruner == nil {
		return errorResult("manage(prune-cache): segment engine unavailable — the client is running in degraded mode")
	}

	// Enumerate the segment-bearing builtins: knowledge/default + every code repo.
	// Mirrors reconcileSegmentCoverage's enumeration, but builds the PARALLEL slices
	// the seam takes (graphTypes[i] pairs with names[i]). Unlike the best-effort
	// reconcile loop, this is an EXPLICIT operator command: a code-repo enumeration
	// error ABORTS the whole op (all-or-nothing on the target set) rather than
	// silently scoping down to knowledge/default.
	//
	// THE TARGET SET IS THEN NARROWED TO THE WORKING SET, and that narrowing is the
	// gate this op owes the operative rule. prune-cache is a manage operation, so it
	// admits nothing, and it is by far the heaviest per-graph reader in the client:
	// PruneCache force-full-loads BOTH pools of every target (forceCompleteLiveSet,
	// prune_cache.go) — for code/platform that is 512 HNSW segments — on the PREVIEW
	// run as much as the executing one, because execute is not consulted until after
	// the live set exists. Running that across every enumerated repo materializes
	// exactly the graphs nothing on this machine maintains.
	//
	// THE FORCE-FULL-LOAD ITSELF IS EXEMPT AND STAYS, and the safety argument is
	// worth restating where the gate is applied rather than leaving it a file away:
	// PruneCache decides what to UNLINK by diffing the on-disk .seg ids against the
	// live set, so a live set that is merely the resident-only view condemns every
	// segment that is not currently loaded and an executing prune DELETES THE WHOLE
	// CORPUS. The load is what makes the diff safe, so it is not something to skip or
	// weaken — it is something to only ever perform on a graph this client is
	// entitled to touch. Narrowing the SET is therefore the whole of the fix; nothing
	// about the load changes.
	graphTypes := []kgtypes.GraphType{}
	names := []string{}
	if inWorkingSetFor(deps, kgtypes.GraphKnowledge, "default") {
		graphTypes = append(graphTypes, kgtypes.GraphKnowledge)
		names = append(names, "default")
	}
	repos, err := ListGraphNamesOfType(ctx, deps, string(kgtypes.GraphCode))
	if err != nil {
		return errorResult(fmt.Sprintf(
			"manage(prune-cache): could not enumerate code repos (%v) — aborted before pruning; "+
				"knowledge/default was not pruned either. Retry once the repo list is reachable.", err))
	}
	var declined []string
	for _, repo := range repos {
		if !inWorkingSetFor(deps, kgtypes.GraphCode, repo) {
			declined = append(declined, "code/"+repo)
			continue
		}
		graphTypes = append(graphTypes, kgtypes.GraphCode)
		names = append(names, repo)
	}

	report, err := pruner.PruneCache(ctx, graphTypes, names, a.Execute)
	if err != nil {
		return errorResult("manage(prune-cache): " + err.Error())
	}
	report.Declined = declined

	if a.Format == "json" {
		return jsonResult(report)
	}
	return textResult(renderPruneCacheReport(report, a.Execute))
}

// renderPruneCacheReport formats the prune-cache report as operator-facing text: a
// headline (DRY RUN would-remove vs. executed removed), then a per-(graph, format)
// pool breakdown, with any List(0)-aborted pools called out so a skipped graph is
// never silent.
func renderPruneCacheReport(report PruneCacheReport, execute bool) string {
	var b strings.Builder

	// Headline. On a preview run the would-remove totals live in the per-pool Orphans/
	// Bytes (Removed/RemovedBytes are zero), so sum the pools for the preview figure.
	var wouldRemove int
	var wouldBytes int64
	pools := 0
	aborted := 0
	for _, g := range report.Graphs {
		if g.Aborted {
			aborted++
			continue
		}
		if len(g.Orphans) > 0 {
			pools++
		}
		wouldRemove += len(g.Orphans)
		wouldBytes += g.Bytes
	}

	if execute {
		fmt.Fprintf(&b, "prune-cache complete: removed %d orphaned segments (%d bytes) across %d graph/format pool(s).\n",
			report.Removed, report.RemovedBytes, pools)
	} else {
		fmt.Fprintf(&b, "DRY RUN — would remove %d orphaned segments (%d bytes) across %d graph/format pool(s). "+
			"Re-run with execute:true to delete.\n", wouldRemove, wouldBytes, pools)
	}

	// Per-pool breakdown.
	for _, g := range report.Graphs {
		if g.Aborted {
			fmt.Fprintf(&b, "  - %s/%s [%s]: SKIPPED — %s\n", g.GraphType, g.Name, g.Format, g.AbortReason)
			continue
		}
		if len(g.Orphans) == 0 {
			continue // no orphans for this pool — omit from the breakdown to keep it terse.
		}
		line := fmt.Sprintf("  - %s/%s [%s]: %d orphan(s), %d bytes", g.GraphType, g.Name, g.Format, len(g.Orphans), g.Bytes)
		// A partial os.Remove failure during execute is surfaced on the pool's
		// AbortReason without setting Aborted (the pool was pruned, just incompletely).
		if g.AbortReason != "" {
			line += " — " + g.AbortReason
		}
		fmt.Fprintln(&b, line)
	}

	if aborted > 0 {
		fmt.Fprintf(&b, "%d pool(s) were SKIPPED by the List(0) subset-abort safety (computed live set incomplete) — nothing was removed for those.\n", aborted)
	}

	// THE DECLINED SET IS NAMED RATHER THAN LEFT TO A ZERO. A graph withheld from
	// the target set contributes no pool at all, so without this line its absence
	// from the breakdown reads exactly like a pool that was scanned and found clean.
	// "Nobody looked" and "nothing to remove" are different facts.
	if len(report.Declined) > 0 {
		fmt.Fprintf(&b, "%d graph(s) were NOT SCANNED because no direct interaction has admitted them on this "+
			"machine (%s). prune-cache is a manage operation and admits nothing, and scanning a graph "+
			"force-loads its whole segment pool — search, collect into or write to a graph and it becomes "+
			"a prune-cache target.\n", len(report.Declined), strings.Join(report.Declined, ", "))
	}
	return b.String()
}
