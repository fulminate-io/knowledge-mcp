// SPDX-License-Identifier: Apache-2.0

// intercept_manage_repair_edges.go — client-side manage(repair_edges) intercept.
// It removes CONTAINS FOSSILS: "a file→symbol CONTAINS edge whose target's
// FilePath differs from the source file node's id". A file node's id IS its
// path (the collector builds it with Id: result.FilePath in
// chunkResultsToPopulate, cmd/knowledge/internal/collector/parser/populate.go),
// so that comparison is between two paths.
//
// An edge whose target carries an EMPTY FilePath is NOT a fossil and is
// excluded: language hub nodes and any node the collector leaves pathless would
// otherwise be swept.
//
// PREVIEW BY DEFAULT. The operation enumerates, reports, and mutates nothing
// unless the operator passes execute=true — the polarity prune-cache uses, and
// deliberately NOT drop_graph's execute-by-default, which is the wrong default
// for an operation that deletes rows from live graphs. It is never auto-run:
// nothing in the collect path, the post-collect linker, or any timer invokes it.
// Against the CLOUD backend an execute run additionally REFUSES an empty name:
// a whole-catalog sweep of production must not be reachable by omitting an
// argument.
//
// A REPAIR COVERS EVERY GRAPH THE READ SURFACE CAN SELECT for the targeted repo
// — the base code graph AND every branch overlay of it — because the reads an
// operator verifies with auto-stamp the checkout's branch and consult the overlay
// stack. branch=<name> narrows to exactly one overlay. Target resolution, the
// bare-overlay-name normalization and the per-target selector live in
// intercept_manage_repair_edges_targets.go.
//
// NO NEW SQL AND NO WIRE CHANGE. The enumeration rides three existing reads per
// target — a keyset node browse, a bulk RETURN_MODE_EDGES pivot read, and a
// by-ids hydrate — and the removal rides the existing MUTATION_KIND_UNLINK +
// EdgeSpec fan, so every server-side statement this capability causes already
// exists and is already tagged.

package tools

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/paging"
)

// repairEdgesSampleCap bounds how many fossils each per-graph section RENDERS.
// The counts beside it are the FULL totals, so a reader can always tell a capped
// sample from a complete listing. Declared once here so the report and the tests
// cite one declaration.
const repairEdgesSampleCap = 15

// repairEdgeFossil is one enumerated cross-file CONTAINS edge: the file node
// claiming the symbol, the symbol it claims, and the file the symbol actually
// lives in.
type repairEdgeFossil struct {
	SourceFile     string
	TargetID       string
	TargetFilePath string
}

// repairGraphResult is one repair TARGET's enumeration — a base graph or one of
// its branch overlays: what was read, and what the predicate kept. FilesScanned
// and EdgesExamined are the denominators that make a fossil count readable — an
// ~846 fossil count against ~150k examined edges is a different fact from the
// same count against 900.
type repairGraphResult struct {
	Target        repairEdgesTarget
	FilesScanned  int
	EdgesExamined int
	Fossils       []repairEdgeFossil
}

// handleClientRepairEdges enumerates the CONTAINS fossils in the targeted code
// graph(s) and renders the report. graph must be "code": the defect is a
// collector containment defect and no other graph type carries file→symbol
// CONTAINS edges. A non-empty name scopes to one repo (its base graph AND every
// branch overlay of it); an empty name sweeps every code graph the server has
// plus each one's overlays; branch=<name> narrows to exactly that one overlay.
// repairEdgesResolveTargets owns that resolution.
//
// ENUMERATE-BEFORE-DELETE IS STRUCTURAL: the execute arm removes exactly the
// list the enumeration produced in THIS invocation, never a predicate evaluated
// server-side over a set nobody enumerated.
func handleClientRepairEdges(ctx context.Context, deps ClientDeps, a manageArgs) kgtools.ToolResult {
	gc := deps.GraphCaller()
	if gc == nil {
		return errorResult("manage(repair_edges): GraphCaller is unavailable — the client is running in degraded mode")
	}
	if a.Graph != string(kgtypes.GraphCode) {
		return errorResult(`manage(repair_edges) requires graph:"code" — CONTAINS fossils are a code-graph defect. ` +
			`Set name=<repo> to scope to one repo, or leave name empty to sweep every code graph.`)
	}
	backend, isCloud := repairEdgesBackend(deps)
	// A whole-catalog sweep of PRODUCTION is not something an operator should be
	// able to trigger by omitting an argument. Local single-graph and local
	// all-graph sweeps stay allowed; so does a NAMED cloud repair.
	if a.Execute && isCloud && a.Name == "" {
		return errorResult(
			"manage(repair_edges): REFUSING an empty-name sweep against " + backend +
				" — these deletes land on live production data. Name the repo explicitly: " +
				`manage(operation:"repair_edges", graph:"code", name:"<repo>", execute:true).`)
	}
	targets, err := repairEdgesResolveTargets(ctx, deps, a)
	if err != nil {
		return errorResult("manage(repair_edges): " + err.Error())
	}
	if len(targets) == 0 {
		return textResult("manage(repair_edges): no code graphs found — nothing to scan.")
	}
	// Serial across graphs is DELIBERATE, not an omission: this is an operator
	// one-shot, the server is the shared resource the page bounds exist to
	// protect, and the pivot drain's own doc (topology/foundation/wire.go)
	// instructs callers not to parallelize pages that share its dedup map.
	results := make([]repairGraphResult, 0, len(targets))
	for _, t := range targets {
		res, rerr := repairEdgesEnumerate(ctx, gc, t)
		if rerr != nil {
			return errorResult("manage(repair_edges): " + rerr.Error())
		}
		results = append(results, res)
	}
	if !a.Execute {
		return textResult(renderRepairEdgesPreview(a, backend, results))
	}
	return textResult(repairEdgesExecute(ctx, gc, backend, results))
}

// repairEdgesBackend names the backend the operation is about to act against.
// The cloud accessor is an OPTIONAL view of ClientDeps (the same structural-
// typing discipline manage(status) uses), so a fixture that does not satisfy it
// reads as local — which is also the safe direction for the empty-name refusal
// only in the sense that a local sweep is genuinely allowed.
func repairEdgesBackend(deps ClientDeps) (label string, isCloud bool) {
	if csi, ok := deps.(cloudStatusInfo); ok {
		if loggedIn, host := csi.CloudStatusInfo(); loggedIn {
			return fmt.Sprintf("the CLOUD backend (%s)", host), true
		}
	}
	return "the LOCAL backend", false
}

// repairEdgesEnumerate reads one repair target — a base graph or one branch
// overlay — and applies the fossil predicate.
//
// THREE BULK READS, not one per file: a keyset browse of the file nodes, ONE
// pivot-paged bulk edges read over the whole file-id set, and a by-ids hydrate
// of the distinct edge targets — O(pages), not O(files).
//
// A read error PROPAGATES rather than degrading to a short set. FetchEdges
// aborts when a single pivot saturates its ceiling page, and a repair computed
// from a silently truncated edge set under-reports fossils — on execute that is
// a partial delete presented as a complete one.
func repairEdgesEnumerate(ctx context.Context, gc GraphCaller, t repairEdgesTarget) (repairGraphResult, error) {
	res := repairGraphResult{Target: t}
	target := repairEdgesSelector(t)

	isFileNode, fileIDs, err := repairEdgesFileNodeIDs(ctx, gc, target)
	if err != nil {
		return res, fmt.Errorf("%s: file-node browse: %w", t.Label(), err)
	}
	res.FilesScanned = len(fileIDs)
	if len(fileIDs) == 0 {
		return res, nil
	}

	edges, err := repairEdgesFetchContainsEdges(ctx, gc, target, fileIDs)
	if err != nil {
		return res, fmt.Errorf("%s: bulk CONTAINS read: %w", t.Label(), err)
	}

	// FetchEdges returns edges incident to the pivot set in BOTH directions, so
	// an incoming package→file CONTAINS arrives here too. The predicate is
	// source-typed: keep only the edges a file node emits.
	outgoing := make([]*knowledgev1.Edge, 0, len(edges))
	targetIDs := make(map[string]bool, len(edges))
	for i := range edges {
		e := &edges[i]
		if !isFileNode[e.GetFromId()] {
			continue
		}
		outgoing = append(outgoing, e)
		targetIDs[e.GetToId()] = true
	}
	res.EdgesExamined = len(outgoing)
	if len(outgoing) == 0 {
		return res, nil
	}

	byID, err := repairEdgesHydrateTargets(ctx, gc, target, targetIDs)
	if err != nil {
		return res, fmt.Errorf("%s: target hydrate: %w", t.Label(), err)
	}
	for _, e := range outgoing {
		source := e.GetFromId()
		// A target that hydrated to nothing has no FilePath, so it falls out on
		// the same branch as a genuinely pathless node — an unresolvable target
		// is never swept.
		targetPath := byID[e.GetToId()].GetFilePath()
		if source == "" || targetPath == "" || targetPath == source {
			continue
		}
		res.Fossils = append(res.Fossils, repairEdgeFossil{
			SourceFile:     source,
			TargetID:       e.GetToId(),
			TargetFilePath: targetPath,
		})
	}
	sort.Slice(res.Fossils, func(i, j int) bool {
		if res.Fossils[i].SourceFile != res.Fossils[j].SourceFile {
			return res.Fossils[i].SourceFile < res.Fossils[j].SourceFile
		}
		return res.Fossils[i].TargetID < res.Fossils[j].TargetID
	})
	return res, nil
}

// repairEdgesFileNodeIDs drains the graph's file nodes in bounded id-keyset
// pages and returns both the membership set the source-typed predicate tests
// and the deduped id slice the bulk edges read pivots on.
func repairEdgesFileNodeIDs(
	ctx context.Context, gc GraphCaller, target *knowledgev1.GraphSelector,
) (map[string]bool, []string, error) {
	files, err := paging.DrainKeysetPages(func(afterID string) ([]*knowledgev1.Node, error) {
		cursor := afterID
		resp, rerr := gc.Execute(ctx, &knowledgev1.ExecuteRequest{
			Plan: &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{
				Selection: &knowledgev1.Selection{NodeType: string(kgtypes.NodeFile)},
				Limit:     int32(paging.BrowsePageSize),
				// SET on every page including the first, where the value is
				// empty: presence is what selects the keyset browse.
				AfterId:   &cursor,
				SkipTotal: true, // the drain never reads Total
			}},
			Target: target,
		})
		if rerr != nil {
			return nil, rerr
		}
		return engine.DecodeNodes(resp)
	}, paging.BrowsePageSize)
	if err != nil {
		return nil, nil, err
	}
	isFileNode := make(map[string]bool, len(files))
	fileIDs := make([]string, 0, len(files))
	for _, n := range files {
		id := n.GetId()
		if id == "" || isFileNode[id] {
			continue
		}
		isFileNode[id] = true
		fileIDs = append(fileIDs, id)
	}
	return isFileNode, fileIDs, nil
}

// repairEdgesHydrateTargets reads the distinct edge targets in bounded by-ids
// pages and returns an id→node map. The predicate needs each target's FilePath,
// which no ids carrier serves, so the nodes stay hydrated and the PAGING is what
// bounds the read — never one lookup per edge.
func repairEdgesHydrateTargets(
	ctx context.Context, gc GraphCaller, target *knowledgev1.GraphSelector, ids map[string]bool,
) (map[string]*knowledgev1.Node, error) {
	list := make([]string, 0, len(ids))
	for id := range ids {
		if id != "" {
			list = append(list, id)
		}
	}
	sort.Strings(list)
	out := make(map[string]*knowledgev1.Node, len(list))
	for start := 0; start < len(list); start += paging.BrowsePageSize {
		end := min(start+paging.BrowsePageSize, len(list))
		resp, err := gc.Execute(ctx, &knowledgev1.ExecuteRequest{
			Plan:   &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{Ids: list[start:end]}},
			Target: target,
		})
		if err != nil {
			return nil, err
		}
		nodes, derr := engine.DecodeNodes(resp)
		if derr != nil {
			return nil, derr
		}
		for _, n := range nodes {
			out[n.GetId()] = n
		}
	}
	return out, nil
}

// repairEdgesUnlinkGroup is one removal round trip: every source file id whose
// CONTAINS edge points at the same fossil target. Grouping by TARGET is what
// makes the removal cost O(distinct targets) rather than O(edges) — the EdgeSpec
// contract fans one relationship+endpoint over the whole selected source set.
type repairEdgesUnlinkGroup struct {
	TargetID string
	Sources  []string
}

// repairEdgesExecute removes the ENUMERATED fossils and re-enumerates to confirm.
// The report NAMES THE BACKEND before any mutation is issued, because on the
// cloud backend these deletes land on live production data.
//
// A non-zero remainder after the unlinks is reported LOUDLY as PARTIAL SUCCESS
// and never swallowed: it is the operator's signal that something re-created or
// blocked the removal.
func repairEdgesExecute(ctx context.Context, gc GraphCaller, backend string, results []repairGraphResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "manage(repair_edges) — EXECUTE against %s.\n\n", backend)
	b.WriteString(renderRepairEdgesBody(results))

	var unlinked int64
	var problems []string
	touched := make([]repairEdgesTarget, 0, len(results))
	for _, r := range results {
		if len(r.Fossils) == 0 {
			continue
		}
		touched = append(touched, r.Target)
		target := repairEdgesSelector(r.Target)
		for _, g := range repairEdgesGroupByTarget(r.Fossils) {
			affected, err := repairEdgesUnlink(ctx, gc, target, g)
			if err != nil {
				problems = append(problems,
					fmt.Sprintf("%s: unlink of %d source(s) → %s FAILED: %v", r.Target.Label(), len(g.Sources), g.TargetID, err))
				continue
			}
			unlinked += affected
		}
	}
	fmt.Fprintf(&b, "\nUnlinked %d CONTAINS edge(s) across %d graph(s).\n", unlinked, len(touched))

	// VERIFY-AFTER: the completion claim is a re-enumeration OF THE SAME TARGET
	// that was just mutated — base or overlay — not "the listed edges were
	// unlinked". One scope for the mutation, the re-read, and the report line.
	var remaining int
	for _, t := range touched {
		after, err := repairEdgesEnumerate(ctx, gc, t)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: re-enumeration FAILED: %v", t.Label(), err))
			continue
		}
		remaining += len(after.Fossils)
		fmt.Fprintf(&b, "  %s: %d fossil(s) remaining after the repair\n", t.Label(), len(after.Fossils))
	}

	if remaining == 0 && len(problems) == 0 {
		b.WriteString("\nRepair COMPLETE — the re-enumeration reports zero remaining fossils.\n")
		return b.String()
	}
	slog.Warn("manage(repair_edges): the repair did not fully complete",
		"backend", backend, "unlinked", unlinked, "remaining", remaining, "problems", len(problems))
	b.WriteString("\nPARTIAL SUCCESS — the repair did NOT fully complete. " +
		"Something re-created or blocked the removal; investigate before re-running.\n")
	for _, p := range problems {
		fmt.Fprintf(&b, "  %s\n", p)
	}
	if remaining > 0 {
		fmt.Fprintf(&b, "  %d fossil(s) still present after the unlinks.\n", remaining)
	}
	return b.String()
}

// repairEdgesGroupByTarget collapses the enumerated fossils into one group per
// distinct target, in a deterministic order.
func repairEdgesGroupByTarget(fossils []repairEdgeFossil) []repairEdgesUnlinkGroup {
	bySource := make(map[string][]string, len(fossils))
	order := make([]string, 0, len(fossils))
	for _, f := range fossils {
		if _, seen := bySource[f.TargetID]; !seen {
			order = append(order, f.TargetID)
		}
		bySource[f.TargetID] = append(bySource[f.TargetID], f.SourceFile)
	}
	sort.Strings(order)
	groups := make([]repairEdgesUnlinkGroup, 0, len(order))
	for _, t := range order {
		groups = append(groups, repairEdgesUnlinkGroup{TargetID: t, Sources: bySource[t]})
	}
	return groups
}

// repairEdgesUnlink issues ONE MUTATION_KIND_UNLINK whose selected set is the
// group's source file ids and whose EdgeSpec names CONTAINS at the fixed target
// endpoint with forward orientation — so each selected file is the edge SOURCE.
// This is the existing unlink mutation, not a new wire shape: no new proto
// message, no new enum value, and server-side no new SQL statement.
func repairEdgesUnlink(
	ctx context.Context, gc GraphCaller, target *knowledgev1.GraphSelector, g repairEdgesUnlinkGroup,
) (int64, error) {
	resp, err := gc.Execute(ctx, &knowledgev1.ExecuteRequest{
		Plan: &knowledgev1.ExecuteRequest_Mutation{Mutation: &knowledgev1.MutationPlan{
			Kind:      knowledgev1.MutationPlan_MUTATION_KIND_UNLINK,
			Selection: &knowledgev1.Selection{Ids: g.Sources},
			EdgeSpec: &knowledgev1.EdgeSpec{
				Relationship: string(kgtypes.EdgeContains),
				ToId:         g.TargetID,
				Forward:      true,
			},
		}},
		Target: target,
	})
	if err != nil {
		return 0, err
	}
	return resp.GetAffectedCount(), nil
}

// renderRepairEdgesPreview renders the read-only report: the backend it would
// act against, per graph the two denominators, the fossil count, and a sample
// capped at repairEdgesSampleCap; then the totals; then the exact invocation
// that would perform the removal.
func renderRepairEdgesPreview(a manageArgs, backend string, results []repairGraphResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "manage(repair_edges) — PREVIEW against %s: nothing was mutated.\n\n", backend)
	b.WriteString(renderRepairEdgesBody(results))
	fmt.Fprintf(&b, "\nTo remove them, re-run: %s\n", repairEdgesExecuteInvocation(a))
	return b.String()
}

// renderRepairEdgesBody renders the per-graph sections and the totals line. It is
// shared by the preview and the execute report so both read the same numbers in
// the same shape.
func renderRepairEdgesBody(results []repairGraphResult) string {
	var b strings.Builder
	var files, examined, fossils int
	for _, r := range results {
		files += r.FilesScanned
		examined += r.EdgesExamined
		fossils += len(r.Fossils)
		// Label() already carries the "code/" prefix and, for an overlay target,
		// the "@branch" suffix — so a per-layer line names the layer it measured.
		fmt.Fprintf(&b, "%s: %d file node(s) scanned, %d CONTAINS edge(s) examined, %d fossil(s) found\n",
			r.Target.Label(), r.FilesScanned, r.EdgesExamined, len(r.Fossils))
		for i, f := range r.Fossils {
			if i >= repairEdgesSampleCap {
				fmt.Fprintf(&b, "  … sample capped at %d of %d fossil(s)\n", repairEdgesSampleCap, len(r.Fossils))
				break
			}
			fmt.Fprintf(&b, "  %s -> %s (lives in %s)\n", f.SourceFile, f.TargetID, f.TargetFilePath)
		}
	}
	fmt.Fprintf(&b, "\nTOTAL across %d graph(s): %d file node(s) scanned, %d CONTAINS edge(s) examined, %d fossil(s) found.\n",
		len(results), files, examined, fossils)
	return b.String()
}

// repairEdgesExecuteInvocation renders the exact call that would perform the
// removal for the target the operator just previewed — the same graph, name and
// branch, with execute:true added.
//
// THE ECHOED branch IS THE BARE FORM, never the operator's raw argument. Two
// spellings appear in this report and they are different things: Label() renders
// a GRAPH IDENTITY ("code/agent@launch-fixes"), while branch: is an ARGUMENT and
// is always bare ("launch-fixes"). Echoing the composed form as an argument would
// make this tool's own output teach the spelling that, before normalization
// landed, resolved to the base and reported a false "Repair COMPLETE".
func repairEdgesExecuteInvocation(a manageArgs) string {
	if a.Name != "" && a.Branch != "" {
		return fmt.Sprintf(`manage(operation:"repair_edges", graph:"code", name:%q, branch:%q, execute:true)`,
			a.Name, bareOverlayName(a.Name, a.Branch))
	}
	if a.Name != "" {
		return fmt.Sprintf(`manage(operation:"repair_edges", graph:"code", name:%q, execute:true)`, a.Name)
	}
	return `manage(operation:"repair_edges", graph:"code", execute:true)`
}
