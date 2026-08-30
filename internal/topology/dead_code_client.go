// SPDX-License-Identifier: Apache-2.0

// Package topology / dead_code_client.go — client-side orchestrator for
// the dead_code analyzer. Wires runRTA (filesystem-side) with the
// node-index fetch (wire RPC to the server's code graph) and returns
// the Findings slice the intercept renders into JSON.
//
// The whole pipeline runs client-side (cmd/knowledge) because packages.Load + SSA + RTA
// all need a real filesystem view of the user's repo.
package topology

import (
	"context"
	"fmt"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

// graphCaller is the narrow interface needed to fetch the code-graph
// node index. Mirrors tools.GraphCaller without creating an import-cycle
// dependency on cmd/knowledge/internal/tools. Execute is the base seam;
// fetchNodeIndex type-asserts it to topoExecutor.
type graphCaller interface {
	Execute(ctx context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error)
}

// topoExecutor is the narrow Execute seam. fetchNodeIndex hands it to
// foundation.FetchNodesByType, whose GraphCaller interface it satisfies
// directly — both declare the identical Execute-only signature, so no adapter
// sits between them. topoExecutor is likewise identical to graphCaller; the
// type-assert keeps the upgrade-or-loud-error path expressed in one place.
type topoExecutor interface {
	Execute(ctx context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error)
}

// RunDeadCode is the client-side entry point invoked by the
// InterceptTopology intercept. It runs runRTA against repoRoot, fetches
// the code graph's function-ish node index by draining bounded keyset pages
// per node type, joins the dead functions to graph node IDs, applies
// reflection-risk classification, and returns the Findings slice.
//
// Errors propagate up; the intercept renders them via errorResult so
// the user sees the same diagnostic shape as the prior server-side
// path. A non-empty diagnostic from runRTA is neither an error nor a silent
// skip: it returns ONE informational finding stating that the analysis did not
// complete, because an empty finding set reads as "this repo has no dead code"
// and a run that analyzed nothing must not be mistaken for a clean one.
func RunDeadCode(ctx context.Context, gc graphCaller, repoRoot, repo string, topK int) ([]foundation.Finding, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("topology/dead_code: %w", err)
	}
	if gc == nil {
		return nil, fmt.Errorf("topology/dead_code: graph caller unavailable")
	}
	if repoRoot == "" {
		return nil, fmt.Errorf("topology/dead_code: repoRoot is required")
	}
	if repo == "" {
		return nil, fmt.Errorf("topology/dead_code: repo is required")
	}

	const tests = true // matches prior server-side default (OQ-3)
	deadFuncs, diagnostic, err := runRTA(ctx, repoRoot, tests)
	if err != nil {
		return nil, fmt.Errorf("rta: %w", err)
	}
	if diagnostic != "" {
		// A diagnostic means the RTA run did not complete: a package that would
		// not load, a build constraint that excluded everything. An empty finding
		// set renders identically to "this repo has no dead code", so the
		// inability is STATED instead — the same loud-over-silent call corpus_scan
		// took when it deviated from this family's skeleton.
		//
		// THIS IS NOT A FALLBACK. Nothing is retried, defaulted or computed some
		// other way; the reported inability IS the honest output.
		return []foundation.Finding{deadCodeIncompleteFinding(repoRoot, diagnostic)}, nil
	}
	if len(deadFuncs) == 0 {
		return nil, nil
	}

	idx, err := fetchNodeIndex(ctx, gc, repo)
	if err != nil {
		return nil, fmt.Errorf("fetch node index: %w", err)
	}

	rows := mapToCodeNodes(ctx, idx, deadFuncs, repoRoot)
	flags := detectReflectionRisk(rows, deadFuncs)

	findings := make([]foundation.Finding, 0, len(rows))
	for i, row := range rows {
		findings = append(findings, buildDeadCodeFinding(row, flags[i]))
	}
	return truncateTopK(findings, topK), nil
}

// DeadCodeIncompleteTitle titles the one informational finding RunDeadCode
// emits when the RTA run produced a diagnostic instead of a call graph. It names
// the analyzer and the reason class, following the disclosure shape corpus_scan
// established; the diagnostic text itself is payload and rides in the summary.
const DeadCodeIncompleteTitle = "dead_code: analysis did not complete"

// deadCodeIncompleteFinding states the RTA run's inability, carrying BOTH the
// diagnostic the run already produced and the root it was run against — the root
// because a reader otherwise cannot tell a genuinely unanalyzable tree from a
// mis-resolved one.
func deadCodeIncompleteFinding(repoRoot, diagnostic string) foundation.Finding {
	return foundation.Finding{
		Algorithm: "dead_code",
		Severity:  foundation.SeverityNotice,
		Title:     DeadCodeIncompleteTitle,
		Summary: fmt.Sprintf("the reachability analysis over %s produced a diagnostic and yielded no call graph, "+
			"so this run reports nothing about dead code rather than reporting none: %s", repoRoot, diagnostic),
		Evidence: []string{repoRoot},
	}
}

// fetchNodeIndex pulls every function-ish node in the scoped code graph by
// delegating one drain per entry of functionishTypes to
// foundation.FetchNodesByType, then keying the union with
// buildNodeIndexFromNodes by (filePath, line).
//
// The per-type shape is load-bearing twice over, which is why this is a
// delegate rather than a cursor bolted onto the previous plural-types browse.
// The plural types key is a POST-FILTER applied after the cap, so on a graph
// holding more than a page of other types that sort first it returns nothing;
// and the plural compile arm threads no after_id, so a keyset drain over it
// would never see a short page and would not terminate. The singular type key
// pushes the filter into the index selection and is the only arm that carries
// the cursor.
//
// The seven drains run serially. Each is internally serial anyway (a keyset
// cursor chains), dead-code analysis is off the latency-critical path, and the
// per-type reads are small — so no concurrency primitive is introduced here.
func fetchNodeIndex(ctx context.Context, gc graphCaller, repo string) (*codeNodeIndex, error) {
	ex, ok := gc.(topoExecutor)
	if !ok {
		return nil, fmt.Errorf("topology/dead_code: requires an Execute-capable graph client")
	}
	var nodes []*knowledgev1.Node
	for _, t := range functionishTypes {
		batch, err := foundation.FetchNodesByType(ctx, ex, kgtypes.GraphCode, repo, kgtypes.NodeType(t))
		if err != nil {
			return nil, fmt.Errorf("fetch %s nodes: %w", t, err)
		}
		nodes = append(nodes, batch...)
	}
	return buildNodeIndexFromNodes(nodes), nil
}

// truncateTopK clips findings to the first k entries when k > 0.
func truncateTopK(findings []foundation.Finding, k int) []foundation.Finding {
	if k <= 0 || len(findings) <= k {
		return findings
	}
	return findings[:k]
}
