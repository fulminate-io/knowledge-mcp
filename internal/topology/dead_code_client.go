// SPDX-License-Identifier: Apache-2.0

// Package topology / dead_code_client.go — client-side orchestrator for
// the dead_code analyzer. Wires runRTA (filesystem-side) with the
// node-index fetch (wire RPC to the server's code graph) and returns
// the Findings slice the intercept renders into JSON.
//
// The whole pipeline runs on the stdio client because packages.Load + SSA + RTA
// all need a real filesystem view of the user's repo.
package topology

import (
	"context"
	"encoding/json"
	"fmt"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

// graphCaller is the narrow interface needed to fetch the code-graph
// node index. Mirrors tools.GraphCaller without creating an import-cycle
// dependency on cmd/knowledge/internal/tools. Execute is the base seam;
// fetchNodeIndex type-asserts it to topoExecutor.
type graphCaller interface {
	Execute(ctx context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error)
}

// topoExecutor is the narrow Execute seam. fetchNodeIndex compiles the
// function-ish code browse to a QueryPlan and decodes the nodes_json carrier. It
// is identical to graphCaller (both require only Execute); the type-assert keeps
// the upgrade-or-loud-error path expressed in one place.
type topoExecutor interface {
	Execute(ctx context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error)
}

// RunDeadCode is the client-side entry point invoked by the
// InterceptTopology intercept. It runs runRTA against repoRoot, fetches
// the code graph's function-ish node index via one wire RPC, joins the
// dead functions to graph node IDs, applies reflection-risk
// classification, and returns the Findings slice.
//
// Errors propagate up; the intercept renders them via errorResult so
// the user sees the same diagnostic shape as the prior server-side
// path. A non-empty diagnostic from runRTA degrades to a nil-findings
// success — dream cycles run this analyzer unattended and a noisy error
// path would spam logs across non-Go repos.
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
	deadFuncs, _, diagnostic, err := runRTA(ctx, repoRoot, tests)
	if err != nil {
		return nil, fmt.Errorf("rta: %w", err)
	}
	if diagnostic != "" {
		// Skip cleanly — same posture as the prior server-side analyzer.
		return nil, nil
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

// fetchNodeIndex issues a single Execute that pulls every function-ish node in
// the scoped code graph. The request is a code-graph PLURAL-types browse
// (`{graph:"code", repo:X, types:[...]}`) that lowers to the plural
// arm (Selection.NodeTypes + postFilterBrowseNodeTypes) on the relaxed code
// guard; the typed Nodes carrier (engine.DecodeNodes) carries the function nodes,
// which buildNodeIndexFromNodes keys by (filePath, line).
func fetchNodeIndex(ctx context.Context, gc graphCaller, repo string) (*codeNodeIndex, error) {
	ex, ok := gc.(topoExecutor)
	if !ok {
		return nil, fmt.Errorf("topology/dead_code: requires an Execute-capable graph client")
	}
	payload := map[string]any{
		"graph": "code",
		"repo":  repo,
		"types": functionishTypes,
		"limit": 0, // no cap — we want every function node
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal query payload: %w", err)
	}
	req, ok := engine.Compile("query", body)
	if !ok {
		return nil, fmt.Errorf("topology/dead_code: node-index query not reducible to an ExecuteRequest")
	}
	resp, err := ex.Execute(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("execute query: %w", err)
	}
	nodes, err := engine.DecodeNodes(resp)
	if err != nil {
		return nil, fmt.Errorf("decode node index: %w", err)
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
