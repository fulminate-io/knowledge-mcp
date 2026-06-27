// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	clienttopo "github.com/fulminate-io/knowledge-mcp/internal/topology"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

// topologyArgs mirrors the subset of query args that the topology
// intercept reads. The full args.Arguments payload is also passed
// through to the server when the intercept falls through, so unrecognized
// fields aren't dropped.
type topologyArgs struct {
	Mode      string            `json:"mode"`
	Algorithm string            `json:"algorithm"`
	Graph     string            `json:"graph"`
	Repo      string            `json:"repo"`
	Branch    string            `json:"branch"`
	Account   string            `json:"account"`
	Name      string            `json:"name"`
	Language  string            `json:"language"`
	TopK      int               `json:"top_k"`
	Extra     map[string]string `json:"extra"`
}

// InterceptTopology runs EVERY topology analyzer client-side. The dead_code
// analyzer routes through the dedicated clienttopo.RunDeadCode RTA path
// (filesystem packages.Load + SSA + RTA); all other analyzers run via the
// foundation registry over the wire — foundation.Get(name) → analyzer.Run with
// a Request whose Caller is the client GraphCaller (the analyzer fetches its own
// nodes/edges over the wire). Nothing falls through to a server Topology RPC: the
// analyzer suite now lives client-side and the server RPC is gone.
//
// dead_code stayed client-side because it needs a real filesystem
// view of the user's repo (the server pod has none); the rest followed by moving
// the rest of the suite client-side too, fetching graph data over the wire so the
// client never operates a store engine.
func InterceptTopology(ctx context.Context, deps ClientDeps, params kgtools.CallToolParams) (bool, kgtools.ToolResult) {
	if params.Name != "query" {
		return false, kgtools.ToolResult{}
	}
	var a topologyArgs
	if err := json.Unmarshal(params.Arguments, &a); err != nil {
		// Decode failures fall through — the server's own error handling
		// gives the user a precise complaint about the JSON body.
		return false, kgtools.ToolResult{}
	}
	if a.Mode != "topology" {
		return false, kgtools.ToolResult{}
	}
	// query(mode:"topology") requires an explicit graph + algorithm. There is no
	// default sweep and no paramless/linkage fallback — both must be named, else a
	// clear validation error is returned (not a vague fallthrough, the old
	// "unknown analyzer \"\"", or empty-graph misbehavior).
	if a.Graph == "" {
		return true, errorResult(`query(mode:"topology") requires "graph" — one of: code, cloud, cicd, knowledge`)
	}
	if a.Algorithm == "" {
		return true, errorResult(`query(mode:"topology") requires "algorithm". Available analyzers: ` + topologyAnalyzerNames())
	}
	// Non-dead_code analyzers run client-side over the wire via the foundation
	// registry; the client renders the findings. dead_code stays on the
	// client-RTA path below (it needs filesystem packages.Load + SSA + RTA).
	if a.Algorithm != "dead_code" {
		return true, runLocalTopology(ctx, deps, a)
	}

	repo, err := resolveTopologyRepo(ctx, deps, a.Repo)
	if err != nil {
		return true, errorResult("topology/dead_code: " + err.Error())
	}

	gc := deps.GraphCaller()
	if gc == nil {
		return true, errorResult("topology/dead_code: graph caller unavailable")
	}

	rootDir := deps.RootDir()
	findings, runErr := clienttopo.RunDeadCode(ctx, gc, rootDir, repo, a.TopK)
	if runErr != nil {
		return true, errorResult("topology/dead_code: " + runErr.Error())
	}

	body, err := json.MarshalIndent(findings, "", "  ")
	if err != nil {
		return true, errorResult("topology/dead_code: marshal findings: " + err.Error())
	}
	return true, textResult(string(body))
}

// runLocalTopology runs one non-dead_code analyzer client-side over the wire and
// renders its findings. It looks the analyzer up in the foundation registry,
// hands it a Request whose Caller is the client GraphCaller (the analyzer fetches
// its own nodes/edges over the wire), and marshals the []foundation.Finding via
// foundation.RenderFindings (the same indented-JSON shape the dead_code path
// uses). The analyzer-instance key rides as Request.Name: code→repo, cloud→account,
// web/pdf→name; knowledge is the single instance (empty Name).
func runLocalTopology(ctx context.Context, deps ClientDeps, a topologyArgs) kgtools.ToolResult {
	gc := deps.GraphCaller()
	if gc == nil {
		return errorResult("topology: graph caller unavailable")
	}
	analyzer, ok := foundation.Get(a.Algorithm)
	if !ok {
		return errorResult(fmt.Sprintf("topology: unknown analyzer %q", a.Algorithm))
	}
	findings, err := analyzer.Run(ctx, foundation.Request{
		Caller:   gc,
		Graph:    kgtypes.GraphType(a.Graph),
		Name:     topologyInstanceName(a),
		RepoRoot: deps.RootDir(),
		TopK:     a.TopK,
		Language: a.Language,
		Extra:    a.Extra,
	})
	if err != nil {
		return errorResult("topology: " + err.Error())
	}
	body, rerr := foundation.RenderFindings(findings)
	if rerr != nil {
		return errorResult("topology: render findings: " + rerr.Error())
	}
	return textResult(body)
}

// topologyInstanceName resolves the per-graph instance key the analyzer reads
// through Request.Name: code graphs key on repo, cloud graphs on account, web/pdf
// on name; knowledge is the single instance (empty). Explicit Name wins when set
// (the wire arg already carries it), then repo, then account.
func topologyInstanceName(a topologyArgs) string {
	switch {
	case a.Name != "":
		return a.Name
	case a.Repo != "":
		return a.Repo
	case a.Account != "":
		return a.Account
	default:
		return ""
	}
}

// topologyAnalyzerNames lists every dispatchable algorithm name for the
// "algorithm is required" error: the foundation registry (already sorted by
// Name) plus the special-cased dead_code RTA path, which does not self-register
// in foundation. Re-sorted for a stable message.
func topologyAnalyzerNames() string {
	all := foundation.All()
	names := make([]string, 0, len(all)+1)
	for _, a := range all {
		names = append(names, a.Name())
	}
	names = append(names, "dead_code")
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// resolveTopologyRepo requires an EXPLICIT repo: topology runs over a named code
// graph, and the client never infers the graph name from cwd. ctx/deps remain in
// the signature for call-site symmetry but are no longer consulted.
func resolveTopologyRepo(_ context.Context, _ ClientDeps, explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	return "", errors.New("repo is required; pass repo (topology runs over a named code graph)")
}
