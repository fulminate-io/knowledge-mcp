// SPDX-License-Identifier: Apache-2.0

// intercept_manage_drop_graph.go — client-side manage(drop_graph) intercept.
// drop_graph tears down a whole non-logs graph (the persisted store plus its
// loaded state) by issuing one MUTATION_KIND_DROP_GRAPH Execute envelope —
// the SAME wire mutation dropLogGraph (tools_logs_manage_graphs.go) fires for
// log graphs. The only delta vs dropLogGraph is the target selector: this
// handler builds it from the operator-supplied (graph, name) via
// manageGraphSelector, so a drop routes the name onto the field each family
// requires (code→Repo, cloud/cicd→Account, practice→Language, else→Name),
// matching the server dropGraphTarget set (engine_mutate_exec.go).
//
// drop_graph deliberately does NOT own log-graph teardown: graph=="logs" is
// rejected with a pointer to manage(discard_logs), which stays the single
// owner of the logs path (its local-engine unregister has no analog here).
//
// DESTRUCTIVE OP: drop_graph mirrors the delete tool's dry_run idiom — the
// default EXECUTES the drop; dry_run:true issues ZERO mutations and renders a
// "would drop" preview so an operator can confirm the target before committing.
//
// The drop is TWO-SIDED: once the server-side mutation succeeds, the handler also
// tears down the graph's LOCAL L2 segment cache (every per-format directory plus
// the rebuild-state record) through the SegmentCacheDropper seam, and the ack
// reports what was actually removed. Leaving those artifacts behind stranded disk
// the operator had no way to reclaim, since prune-cache only reaches graphs that
// still exist.

package tools

import (
	"context"
	"fmt"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/workingset"
)

// dropGraphFamilies names the graph families the server dropGraphTarget arm
// accepts (engine_mutate_exec.go:217-271), surfaced verbatim in the gate
// errors so the client message matches the server's accepted set.
const dropGraphFamilies = "knowledge, code, cloud, cicd, practice, web, pdf, checks, linkage, or a registered custom type"

// handleClientDropGraph tears down a whole non-logs graph. It requires a
// non-empty graph, rejects graph=="logs" (manage(discard_logs) owns that
// path), and — unless dry_run is set — issues ONE MUTATION_KIND_DROP_GRAPH
// Execute whose Target is the manageGraphSelector envelope, then hands off to
// dropGraphAck for the local cache teardown and the result message. dry_run:true
// renders a read-only "would drop" preview, issues no mutation and removes
// nothing locally.
func handleClientDropGraph(ctx context.Context, deps ClientDeps, a manageArgs) kgtools.ToolResult {
	gc := deps.GraphCaller()
	if gc == nil {
		return errorResult("manage(drop_graph): GraphCaller is unavailable — the client is running in degraded mode")
	}
	if a.Graph == "" {
		return errorResult(fmt.Sprintf(
			`manage(drop_graph) requires "graph" — name the graph to drop (%s)`, dropGraphFamilies))
	}
	if a.Graph == "logs" {
		return errorResult(
			"manage(drop_graph) does not own log graphs — use manage(discard_logs, name:<query_id>) to drop a log graph")
	}

	if a.DryRun {
		// Nothing below this branch runs: no mutation is issued AND the local cache
		// is never touched or even inspected. The preview must also not contain the
		// substring "Dropped graph" — a reader (and the dry-run test) treats that
		// phrase as the claim a drop COMPLETED.
		return textResult(fmt.Sprintf(
			"DRY RUN — would drop graph %s server-side and remove its local segment cache "+
				"(nothing was dropped). Re-run without dry_run to drop.",
			dropGraphLabel(a)))
	}

	ex, err := persistExecutor(gc)
	if err != nil {
		return errorResult("manage(drop_graph): " + err.Error())
	}
	_, eerr := ex.Execute(ctx, &knowledgev1.ExecuteRequest{
		Plan: &knowledgev1.ExecuteRequest_Mutation{
			Mutation: &knowledgev1.MutationPlan{
				Kind: knowledgev1.MutationPlan_MUTATION_KIND_DROP_GRAPH,
			},
		},
		Target: manageGraphSelector(a.Graph, a.Name),
	})
	if eerr != nil {
		return errorResult("manage(drop_graph): " + eerr.Error())
	}
	return textResult(dropGraphAck(deps, a))
}

// dropGraphAck tears down the dropped graph's LOCAL L2 segment cache and renders
// what actually happened. It runs only after the server-side Execute succeeded,
// which is the whole contract: local removal FOLLOWS a confirmed server-side drop.
//
// The ack reports the removal, never a count from the Execute response — the
// server returns ONE handle for the dropped graph, so rendering it as a node count
// told an operator who dropped a 40k-node graph that one node went away.
//
// Every variant here keeps the "Dropped graph <label>" prefix: the graph really is
// gone server-side in all four, and none of them is an error result. In particular
// a local cleanup failure stays a text result — reporting an error for work that
// succeeded would send an operator hunting a drop that already happened.
//
// There is deliberately no PipelineReady() gate (unlike manage(prune-cache), which
// errors during the bind-first startup window): a nil dropper degrades to an honest
// "not inspected" line rather than failing a completed drop.
//
// THE WORKING-SET REMOVAL IS THE FIRST STATEMENT, AHEAD OF THE NIL-DROPPER RETURN,
// and the position is load-bearing rather than tidy. That return sits above the
// line resolving the cache target, so a removal placed beside the cache teardown
// is skipped ENTIRELY on a client with no segment engine — a client that has
// still taken the server-side drop and must still stop wanting the graph. Placed
// here it runs on every path that reaches this function.
//
// ORDERING IS THE CRASH-WINDOW ARGUMENT. Forgetting membership BEFORE tearing the
// cache down means a crash between the two leaves a graph the client no longer
// wants beside a cache the existing prune path can still reach. The reverse order
// leaves a WANTED graph with no cache, which pays a full-layer CorpusDelta walk
// every tick. Both are recoverable; only one is silent work.
//
// IT CANNOT REACH THE DRY-RUN BRANCH. handleClientDropGraph returns inside its
// DryRun branch well above the Execute, and this function has exactly one
// production call site, on the success path after that Execute succeeded.
func dropGraphAck(deps ClientDeps, a manageArgs) string {
	label := dropGraphLabel(a)
	gt, cacheName := dropGraphCacheTarget(a)
	removeFromWorkingSetFor(deps, gt, cacheName)

	dropper := deps.SegmentCacheDropper()
	if dropper == nil {
		return fmt.Sprintf(
			"Dropped graph %s — server-side graph removed; "+
				"local segment cache not inspected (segment engine not wired).", label)
	}

	report, derr := dropper.DropGraphCache(gt, cacheName)
	if derr != nil {
		return fmt.Sprintf(
			"Dropped graph %s — server-side graph removed; local segment cache cleanup "+
				"INCOMPLETE: %v (removed %d file(s), %d bytes).",
			label, derr, report.Files, report.Bytes)
	}
	if len(report.Formats) == 0 {
		return fmt.Sprintf(
			"Dropped graph %s — server-side graph removed; no local segment cache artifacts found.", label)
	}
	return fmt.Sprintf(
		"Dropped graph %s — server-side graph removed; local segment cache: %d file(s), %d bytes across %s.",
		label, report.Files, report.Bytes, strings.Join(report.Formats, ", "))
}

// dropGraphCacheTarget resolves the (graphType, name) pair the local segment cache
// is keyed by. Only the knowledge graph needs normalizing: its cache lives at
// <root>/<format>/knowledge/default, so a bare or self-named knowledge target maps
// to "default" — the SAME rule manageGraphSelector applies (intercept_manage_index.go)
// and handleClientRebuildSegments defaults to.
//
// EVERY SINGLE-INSTANCE FAMILY RESOLVES ITS EMPTY NAME, not just knowledge. This
// used to say no per-family switch was needed because every other family carries
// its instance in a.Name — true while knowledge was the only graph addressed
// without one. The checks graph is addressed by NO name at all, so it fell
// through with an empty key while its segments were cached under the canonical
// instance, and dropping its cache silently dropped nothing.
//
// The remaining families are unaffected: canonicalization is the identity
// wherever a real instance field exists (code→repo, cloud/cicd→account,
// practice→language, custom→name), and the cache path keys on that same string.
func dropGraphCacheTarget(a manageArgs) (kgtypes.GraphType, string) {
	gt := kgtypes.GraphType(a.Graph)
	name := a.Name
	if a.Graph == string(kgtypes.GraphKnowledge) && name == string(kgtypes.GraphKnowledge) {
		name = workingset.DefaultInstanceName
	}
	return gt, workingset.CanonicalInstanceName(gt, name)
}

// dropGraphLabel renders the (graph, name) target for the ack / preview line.
// A bare graph (e.g. the knowledge default) renders as just the graph; a named
// instance renders as graph/name. Mirrors renderPruneAck's target form.
func dropGraphLabel(a manageArgs) string {
	if a.Name != "" {
		return fmt.Sprintf("%s/%s", a.Graph, a.Name)
	}
	return a.Graph
}
