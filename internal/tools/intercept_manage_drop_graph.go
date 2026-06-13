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

package tools

import (
	"context"
	"fmt"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// dropGraphFamilies names the graph families the server dropGraphTarget arm
// accepts (engine_mutate_exec.go:217-271), surfaced verbatim in the gate
// errors so the client message matches the server's accepted set.
const dropGraphFamilies = "knowledge, code, cloud, cicd, practice, web, pdf, transformers, linkage, or a registered custom type"

// handleClientDropGraph tears down a whole non-logs graph. It requires a
// non-empty graph, rejects graph=="logs" (manage(discard_logs) owns that
// path), and — unless dry_run is set — issues ONE MUTATION_KIND_DROP_GRAPH
// Execute whose Target is the manageGraphSelector envelope. dry_run:true
// renders a read-only "would drop" preview and issues no mutation.
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
		return textResult(fmt.Sprintf(
			"DRY RUN — would drop graph %s (nothing was dropped). Re-run without dry_run to drop.",
			dropGraphLabel(a)))
	}

	ex, err := persistExecutor(gc)
	if err != nil {
		return errorResult("manage(drop_graph): " + err.Error())
	}
	resp, eerr := ex.Execute(ctx, &knowledgev1.ExecuteRequest{
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
	return textResult(fmt.Sprintf(
		"Dropped graph %s (%d node(s) removed).", dropGraphLabel(a), resp.GetAffectedCount()))
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
