// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"fmt"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/projects/render"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
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
// Scoping mirrors resolveClearLLMFailuresTargets:
//   - graph+name: target one graph only.
//   - graph only: every loaded graph of that type.
//   - neither: every loaded graph across the LLM-eligible types.
//
// Per-graph clear is bounded-constant: exactly TWO Execute UPDATEs regardless of
// how many nodes carry the marker (the engine resolves the predicate write-WHERE
// id-set inside one serializable txn — engine executeMutationPlan).
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
	)
	for _, tgt := range targets {
		sum, emb, cerr := clearLLMFailuresInGraph(ctx, ex, tgt)
		if cerr != nil {
			// Per-graph failures do not abort the sweep — operator usually wants
			// partial results when one graph is unrecoverable (parity with the
			// server's continue-on-per-graph-error loop).
			continue
		}
		totalSummaryCleared += sum
		totalEmbedCleared += emb
		if sum > 0 || emb > 0 {
			perGraph = append(perGraph, fmt.Sprintf("%s/%s=%d/%d", tgt.graphType, tgt.name, sum, emb))
		}
	}

	if totalSummaryCleared == 0 && totalEmbedCleared == 0 {
		return textResult(fmt.Sprintf("clear_llm_failures: no failure markers found across %d graph(s)", len(targets)))
	}
	if pr, ok := deps.(pipelineResetter); ok {
		pr.ResetPipelineFailedCounters()
	}
	return textResult(fmt.Sprintf("clear_llm_failures: cleared %d summary marker(s) + %d embed marker(s) across %d graph(s): %v",
		totalSummaryCleared, totalEmbedCleared, len(perGraph), perGraph))
}

// clearLLMFailureTarget is one (graph_type, name) pair to clear. graphType ""
// targets the knowledge/default graph.
type clearLLMFailureTarget struct {
	graphType string
	name      string
}

// resolveClearLLMFailureTargets enumerates the graphs to clear from the
// operator scoping, mirroring the server resolveClearLLMFailuresTargets.
func resolveClearLLMFailureTargets(ctx context.Context, deps ClientDeps, a manageArgs) ([]clearLLMFailureTarget, error) {
	if a.Graph != "" {
		if a.Name != "" {
			return []clearLLMFailureTarget{{graphType: a.Graph, name: a.Name}}, nil
		}
		names, err := listGraphNamesOfType(ctx, deps, a.Graph)
		if err != nil {
			return nil, err
		}
		return targetsForType(a.Graph, names), nil
	}
	// Empty graph: walk every loaded graph across every LLM-eligible type.
	var out []clearLLMFailureTarget
	for _, gt := range llmFailureGraphTypes {
		names, err := listGraphNamesOfType(ctx, deps, gt)
		if err != nil {
			return nil, err
		}
		out = append(out, targetsForType(gt, names)...)
	}
	return out, nil
}

// targetsForType turns a graph-type + name list into clear targets. The wire
// name is preserved verbatim for the per-graph summary; clearTarget owns the
// knowledge-root → nil-selector normalization at Execute time.
func targetsForType(graphType string, names []string) []clearLLMFailureTarget {
	out := make([]clearLLMFailureTarget, 0, len(names))
	for _, n := range names {
		out = append(out, clearLLMFailureTarget{graphType: graphType, name: n})
	}
	return out
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
// nil selector — the engine treats an absent/empty graph as the knowledge root
// (parity with the server's empty-graph default). Practice routes the name via
// Language; every other graph via Name (mirrors graphTarget in render).
func clearTarget(tgt clearLLMFailureTarget) *knowledgev1.GraphSelector {
	if tgt.graphType == "" || tgt.graphType == string(kgtypes.GraphKnowledge) {
		if isKnowledgeRootName(tgt.name) {
			return nil
		}
		return &knowledgev1.GraphSelector{Graph: tgt.graphType, Name: tgt.name}
	}
	sel := &knowledgev1.GraphSelector{Graph: tgt.graphType}
	if tgt.graphType == string(kgtypes.GraphPractice) {
		sel.Language = tgt.name
	} else {
		sel.Name = tgt.name
	}
	return sel
}

// isKnowledgeRootName reports whether the wire name refers to the knowledge root
// graph (empty, or the "knowledge"/"default" aliases the overview reports).
func isKnowledgeRootName(name string) bool {
	return name == "" || name == "knowledge" || name == "default"
}
