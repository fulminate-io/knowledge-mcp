// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"fmt"
	"log/slog"
	"slices"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/projects/render"
)

// llmFailureKeys are the two LLM-pipeline failure markers cleared by
// clear_llm_failures. Stored as inline metadata; clearing means writing the
// empty string (the explicit "no failure" state the discovery shim looks for —
// NOT a delete). Mirrors the server's clearLLMFailuresInGraph
// (tools_manage_clear_llm_failures.go).
var llmFailureKeys = []string{
	kgtypes.MetaKeySummaryFailureReason,
	kgtypes.MetaKeyEmbedFailureReason,
}

// llmFailureGraphTypes are the LLM-eligible graph types swept when no graph is
// specified. Mirrors the server resolveClearLLMFailuresTargets list: logs / web
// / linkage / transformers never participate in the LLM pipeline so they cannot
// accumulate failure markers.
var llmFailureGraphTypes = []string{
	string(kgtypes.GraphKnowledge),
	string(kgtypes.GraphCode),
	string(kgtypes.GraphPractice),
	string(kgtypes.GraphCloud),
	string(kgtypes.GraphCICD),
}

// handleClientClearLLMFailures clears the two LLM-pipeline failure markers
// (summary_failure_reason + embed_failure_reason) across the resolved graph
// target(s) by composing TWO generic MutationPlan UPDATEs per graph — each an
// OP_EXISTS predicate write-WHERE (select nodes carrying the marker) plus a
// set_metadata empty-string clear. This reproduces the server
// handleClearLLMFailures semantics (same nodes selected + cleared) using only
// the generic Execute toolbox; NOT an Index/manage server op.
//
// Scoping (resolveClearLLMFailureTargets) follows the server's base-resolution
// shape AND additionally fans each resolved base out across its overlay keys so
// overlay-RESIDENT markers are reachable (the count path already iterates
// per-overlay-key):
//   - graph+name: the named base PLUS each of its overlay keys.
//   - graph only: every loaded base of that type, each plus its overlay keys.
//   - neither: every loaded base across the LLM-eligible types, each plus overlays.
//
// Per-graph clear is bounded-constant: exactly TWO Execute UPDATEs per resolved
// key regardless of how many nodes carry the marker (the engine resolves the
// predicate write-WHERE id-set inside one serializable txn — engine
// executeMutationPlan).
func handleClientClearLLMFailures(ctx context.Context, deps ClientDeps, a manageArgs) kgtools.ToolResult {
	gc := deps.GraphCaller()
	if gc == nil {
		return errorResult("manage(clear_llm_failures): GraphCaller is unavailable — the client is running in degraded mode")
	}
	ex, err := persistExecutor(gc)
	if err != nil {
		return errorResult("manage(clear_llm_failures): " + err.Error())
	}

	targets, terr := resolveClearLLMFailureTargets(ctx, deps, a)
	if terr != nil {
		return errorResult("manage(clear_llm_failures): " + terr.Error())
	}
	if len(targets) == 0 {
		return textResult("no loaded graphs match the request — nothing to clear")
	}

	var (
		totalSummaryCleared int64
		totalEmbedCleared   int64
		perGraph            []string
		perGraphErrors      []string
	)
	for _, tgt := range targets {
		sum, emb, cerr := clearLLMFailuresInGraph(ctx, ex, tgt)
		if cerr != nil {
			// Per-graph failures do not abort the sweep — operator usually wants
			// partial results when one graph is unrecoverable (parity with the
			// server's continue-on-per-graph-error loop). The error is BOTH logged
			// AND collected into the operator-visible result so a systemic failure
			// can't hide behind a "0 found" summary (ticket item 4).
			slog.Warn("manage(clear_llm_failures): per-graph clear failed",
				"graph_type", tgt.graphType, "name", targetLabel(tgt), "error", cerr)
			perGraphErrors = append(perGraphErrors,
				fmt.Sprintf("%s/%s=%v", tgt.graphType, targetLabel(tgt), cerr))
			continue
		}
		totalSummaryCleared += sum
		totalEmbedCleared += emb
		if sum > 0 || emb > 0 {
			perGraph = append(perGraph, fmt.Sprintf("%s/%s=%d/%d", tgt.graphType, targetLabel(tgt), sum, emb))
		}
	}

	if totalSummaryCleared == 0 && totalEmbedCleared == 0 {
		if len(perGraphErrors) > 0 {
			// An all-zero sweep that hit errors is NOT a clean "nothing to clear" —
			// surface it as an error naming every failing target.
			return errorResult(fmt.Sprintf("clear_llm_failures: no markers cleared and %d graph(s) errored: %v",
				len(perGraphErrors), perGraphErrors))
		}
		return textResult(fmt.Sprintf("clear_llm_failures: no failure markers found across %d graph(s)", len(targets)))
	}
	if pr, ok := deps.(pipelineResetter); ok {
		pr.ResetPipelineFailedCounters()
	}
	summary := fmt.Sprintf("clear_llm_failures: cleared %d summary marker(s) + %d embed marker(s) across %d graph(s): %v",
		totalSummaryCleared, totalEmbedCleared, len(perGraph), perGraph)
	if len(perGraphErrors) > 0 {
		// Partial success: cleared some markers but other graphs errored. Append the
		// errors to the operator-visible text so they are not swallowed.
		summary += fmt.Sprintf("; errors: %v", perGraphErrors)
	}
	return textResult(summary)
}

// clearLLMFailureTarget is one (graph_type, name, branch) triple to clear.
// graphType "" targets the knowledge/default graph. branch is the OVERLAY
// dimension: empty == the base graph (today's behavior); non-empty == a single
// overlay key of that base. For code it rides GraphSelector.Branch (composed to
// repo@branch by the server); for other types it composes base@overlay onto the
// name (the server Scope @-split resolves it).
type clearLLMFailureTarget struct {
	graphType string
	name      string
	branch    string
}

// resolveClearLLMFailureTargets enumerates the graphs to clear from the
// operator scoping, mirroring the server resolveClearLLMFailuresTargets, AND
// fans out across every overlay key of each resolved base so overlay-RESIDENT
// markers are reachable (the count path already iterates per-overlay-key —
// reads.go). For each base it appends the base target PLUS one target per overlay
// key (split base@overlay → {name:base, branch:overlay}).
func resolveClearLLMFailureTargets(ctx context.Context, deps ClientDeps, a manageArgs) ([]clearLLMFailureTarget, error) {
	if a.Graph != "" && a.Name != "" {
		return resolveNamedClearTarget(ctx, deps, a.Graph, a.Name)
	}
	if a.Graph != "" {
		names, err := listGraphNamesOfType(ctx, deps, a.Graph)
		if err != nil {
			return nil, err
		}
		return targetsForType(ctx, deps, a.Graph, names)
	}
	// Empty graph: walk every loaded graph across every LLM-eligible type.
	var out []clearLLMFailureTarget
	for _, gt := range llmFailureGraphTypes {
		names, err := listGraphNamesOfType(ctx, deps, gt)
		if err != nil {
			return nil, err
		}
		typed, err := targetsForType(ctx, deps, gt, names)
		if err != nil {
			return nil, err
		}
		out = append(out, typed...)
	}
	return out, nil
}

// resolveNamedClearTarget resolves a SPECIFIC graph+name clear target. It FIRST
// verifies the name resolves against the enumerated catalog (so an unresolvable
// name fails loud instead of the silent "no failure markers found across 1
// graph(s)"), then returns the named base PLUS its overlay keys. When the name is
// itself an overlay key (base@overlay) the overlay enumeration over it yields
// nothing, leaving just the named target — which is correct.
func resolveNamedClearTarget(ctx context.Context, deps ClientDeps, graph, name string) ([]clearLLMFailureTarget, error) {
	ok, err := clearTargetResolves(ctx, deps, graph, name)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("graph target %q not found in the %s catalog", name, graph)
	}
	out := []clearLLMFailureTarget{{graphType: graph, name: name}}
	return appendOverlayTargets(ctx, deps, out, graph, name)
}

// clearTargetResolves reports whether a SPECIFIC graph+name target exists in the
// enumerated catalog: either a base name (listGraphNamesOfType) or an overlay key
// "base@overlay" of one of those bases (listOverlayKeysOfBase). It is the
// membership test the graph+name arm runs before issuing any UPDATE so an
// unresolvable name fails loud rather than reporting "0 found".
func clearTargetResolves(ctx context.Context, deps ClientDeps, graphType, name string) (bool, error) {
	names, err := listGraphNamesOfType(ctx, deps, graphType)
	if err != nil {
		return false, err
	}
	if slices.Contains(names, name) {
		return true, nil
	}
	// Not a base name — if it is an overlay key (base@overlay), verify the overlay
	// against the base's overlay enumeration.
	base, overlay, ok := atSplit(name)
	if !ok || overlay == "" {
		return false, nil
	}
	keys, err := listOverlayKeysOfBase(ctx, deps, graphType, base)
	if err != nil {
		return false, err
	}
	return slices.Contains(keys, name), nil
}

// targetsForType turns a graph-type + name list into clear targets, fanning each
// base out across its overlay keys. The wire name is preserved verbatim for the
// per-graph summary; clearTarget owns the knowledge-root → nil-selector
// normalization at Execute time.
func targetsForType(ctx context.Context, deps ClientDeps, graphType string, names []string) ([]clearLLMFailureTarget, error) {
	out := make([]clearLLMFailureTarget, 0, len(names))
	for _, n := range names {
		out = append(out, clearLLMFailureTarget{graphType: graphType, name: n})
		var err error
		out, err = appendOverlayTargets(ctx, deps, out, graphType, n)
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// appendOverlayTargets enumerates the overlay keys of one base graph and appends
// a clear target per overlay (split base@overlay → {name:base, branch:overlay}).
// Returns the grown slice. An overlay key whose split does not match the base is
// skipped defensively (the enumeration is base-scoped, so this is belt-and-braces).
func appendOverlayTargets(ctx context.Context, deps ClientDeps, out []clearLLMFailureTarget, graphType, base string) ([]clearLLMFailureTarget, error) {
	keys, err := listOverlayKeysOfBase(ctx, deps, graphType, base)
	if err != nil {
		return nil, err
	}
	for _, key := range keys {
		b, overlay, ok := atSplit(key)
		// Defensive: the enumeration is base-scoped, so every key should be
		// base@overlay; skip anything that doesn't split or doesn't match the base.
		if !ok || overlay == "" || b != base {
			continue
		}
		out = append(out, clearLLMFailureTarget{graphType: graphType, name: b, branch: overlay})
	}
	return out, nil
}

// atSplit splits a "left@right" string at the FIRST "@" into (left, right, true).
// Returns ("", "", false) when there is no "@".
func atSplit(s string) (string, string, bool) {
	for i := 0; i < len(s); i++ {
		if s[i] == '@' {
			return s[:i], s[i+1:], true
		}
	}
	return "", "", false
}

// clearLLMFailuresInGraph issues the TWO predicate UPDATEs (one per failure
// marker) against a single graph and returns the (summaryCleared, embedCleared)
// affected counts. Each UPDATE is an OP_EXISTS predicate write-WHERE +
// set_metadata empty clear.
func clearLLMFailuresInGraph(ctx context.Context, ex render.Executor, tgt clearLLMFailureTarget) (sumCleared int64, embCleared int64, err error) {
	counts := make([]int64, len(llmFailureKeys))
	for i, key := range llmFailureKeys {
		n, cerr := execClearMarkerUpdate(ctx, ex, tgt, key)
		if cerr != nil {
			return 0, 0, cerr
		}
		counts[i] = n
	}
	return counts[0], counts[1], nil
}

// execClearMarkerUpdate runs ONE predicate UPDATE clearing a single failure
// marker key across the target graph: Selection.metadata_predicates carries the
// OP_EXISTS predicate (select nodes with the marker set) and set_metadata writes
// the empty string. Returns the affected_count the engine reports.
func execClearMarkerUpdate(ctx context.Context, ex render.Executor, tgt clearLLMFailureTarget, key string) (int64, error) {
	plan := &knowledgev1.MutationPlan{
		Kind: knowledgev1.MutationPlan_MUTATION_KIND_UPDATE,
		Selection: &knowledgev1.Selection{
			MetadataPredicates: []*knowledgev1.MetadataPredicate{
				{Key: key, Op: knowledgev1.MetadataPredicate_OP_EXISTS},
			},
		},
		SetMetadata: map[string]string{key: ""},
	}
	resp, err := ex.Execute(ctx, &knowledgev1.ExecuteRequest{
		Plan:   &knowledgev1.ExecuteRequest_Mutation{Mutation: plan},
		Target: clearTarget(tgt),
	})
	if err != nil {
		return 0, fmt.Errorf("clear %s in %s/%s: %w", key, tgt.graphType, tgt.name, err)
	}
	return resp.GetAffectedCount(), nil
}

// clearTarget builds the GraphSelector for one clear target. The knowledge
// root graph (empty graphType, or graph==knowledge with the root name) maps to a
// nil selector when targeting the BASE — the engine treats an absent/empty graph
// as the knowledge root (parity with the server's empty-graph default). Other
// graph types each carry their name discriminant on a TYPE-SPECIFIC GraphSelector
// field (Repo for code, Account for cloud/cicd, Language for practice, Name for
// logs/web/pdf/transformers and the knowledge root). The server-side resolver
// enforces the right field per graph type and rejects a sel.Name on a code graph
// with "graph=code requires repo: graph selector invalid", so the discriminant
// choice here is load-bearing.
//
// The OVERLAY dimension (tgt.branch): for code it rides GraphSelector.Branch
// (the server composes repo@branch and Scopes to the overlay — tools_graph_routing.go:214);
// for every other type the base@overlay is composed onto the type-specific name
// discriminant (the server Scope @-split resolves it — cloud lifecycle.go,
// composite_db_lifecycle.go). Empty branch == the base graph (today's behavior),
// so a base-only target with an empty branch still maps to the nil knowledge-root
// selector.
func clearTarget(tgt clearLLMFailureTarget) *knowledgev1.GraphSelector {
	if tgt.graphType == "" || tgt.graphType == string(kgtypes.GraphKnowledge) {
		if isKnowledgeRootName(tgt.name) && tgt.branch == "" {
			return nil
		}
		return &knowledgev1.GraphSelector{Graph: tgt.graphType, Name: overlayName(tgt.name, tgt.branch)}
	}
	sel := &knowledgev1.GraphSelector{Graph: tgt.graphType}
	switch tgt.graphType {
	case string(kgtypes.GraphCode):
		// Code routes the overlay via the dedicated Branch field (repo@branch),
		// NOT by composing onto Repo.
		sel.Repo = tgt.name
		sel.Branch = tgt.branch
	case string(kgtypes.GraphCloud), string(kgtypes.GraphCICD):
		sel.Account = overlayName(tgt.name, tgt.branch)
	case string(kgtypes.GraphPractice):
		sel.Language = overlayName(tgt.name, tgt.branch)
	default:
		sel.Name = overlayName(tgt.name, tgt.branch)
	}
	return sel
}

// overlayName composes "base@overlay" when overlay is non-empty, else returns
// base unchanged. The server's Scope @-split recovers the overlay suffix.
func overlayName(base, overlay string) string {
	if overlay == "" {
		return base
	}
	return base + "@" + overlay
}

// targetLabel renders the operator-visible name for one target in the per-graph
// summary, surfacing the overlay key (name@overlay) when the target is an overlay.
func targetLabel(tgt clearLLMFailureTarget) string {
	return overlayName(tgt.name, tgt.branch)
}

// isKnowledgeRootName reports whether the wire name refers to the knowledge root
// graph (empty, or the "knowledge"/"default" aliases the overview reports).
func isKnowledgeRootName(name string) bool {
	return name == "" || name == "knowledge" || name == "default"
}
