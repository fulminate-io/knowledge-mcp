// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	clienttopo "github.com/fulminate-io/knowledge-mcp/internal/topology"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/corpusscan"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

// topologyArgs mirrors the subset of query args that the topology
// intercept reads. The full args.Arguments payload is also passed
// through to the server when the intercept falls through, so unrecognized
// fields aren't dropped.
type topologyArgs struct {
	Mode       string            `json:"mode"`
	Algorithm  string            `json:"algorithm"`
	Graph      string            `json:"graph"`
	Repo       string            `json:"repo"`
	Branch     string            `json:"branch"`
	Account    string            `json:"account"`
	Name       string            `json:"name"`
	Language   string            `json:"language"`
	PathPrefix string            `json:"path_prefix"`
	TopK       int               `json:"top_k"`
	Extra      map[string]string `json:"extra"`
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
	if err := accountQueryParams(armTopology, params.Arguments); err != nil {
		return true, errorResult(err.Error())
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

	// UNCONDITIONAL here, unlike the conditional resolve in runLocalTopology:
	// resolveTopologyRepo above has already required a non-empty repo, so there is
	// no empty case to protect and an allowlist consult would only add a way to
	// get it wrong.
	rootDir, rootErr := resolveRepoDir(ctx, deps, "topology/dead_code", repo)
	if rootErr != nil {
		return true, errorResult(rootErr.Error())
	}
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

// pathPrefixHonoringAnalyzers names every analyzer that reads
// Request.PathPrefix. A path_prefix supplied for any other algorithm is
// REFUSED rather than routed: routing it would hand the caller a control the
// other analyzers ignore, which is the same silent-drop defect this fix exists
// to close, multiplied. It is a var rather than a const map so a test can add
// its own probe analyzer without a production file ever naming a test symbol.
var pathPrefixHonoringAnalyzers = map[string]bool{corpusscan.AnalyzerName: true}

// repoRootRequiringAnalyzers names every foundation analyzer that reads
// Request.RepoRoot — the ones that walk a tree off disk rather than only reading
// the graph over the wire. Its members are the complete census of RepoRoot
// readers under cmd/knowledge/internal/topology: corpus_scan (exec_ast.go's
// ast.Match, scan.go's required-root check) and dsm (go.mod and the layer config
// under the root).
//
// THE CONSULT IS WHAT MAKES THE RESOLVE CONDITIONAL, and that is the whole
// design. resolveRepoDir has a fail-loud floor for an unresolvable repo, so
// resolving unconditionally would break every knowledge and cloud analyzer —
// those carry no repo at all and have no tree to walk. It is a var rather than a
// const map for the same reason the path_prefix map above is: a test registers
// its own probe analyzer without a production file ever naming a test symbol.
var repoRootRequiringAnalyzers = map[string]bool{corpusscan.AnalyzerName: true, "dsm": true}

// pathPrefixHonoringNames renders the honoring set sorted, so the refusal
// message is stable across runs rather than following map iteration order.
func pathPrefixHonoringNames() string {
	names := make([]string, 0, len(pathPrefixHonoringAnalyzers))
	for name := range pathPrefixHonoringAnalyzers {
		names = append(names, name)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
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
	// The refusal runs AFTER the registry lookup deliberately: an unknown
	// algorithm must still report "unknown analyzer" rather than a path_prefix
	// complaint about a name that does not exist.
	if a.PathPrefix != "" && !pathPrefixHonoringAnalyzers[a.Algorithm] {
		return errorResult(fmt.Sprintf(
			"topology: path_prefix is not honored by analyzer %q — it narrows the walk only for: %s",
			a.Algorithm, pathPrefixHonoringNames(),
		))
	}
	// The walk root is resolved from the repo argument for the analyzers that
	// declare they read it, and left at the daemon root for every other one —
	// that retained deps.RootDir() is this file's single sanctioned survivor.
	// The branch sits AFTER the registry lookup and the path_prefix refusal so an
	// unknown algorithm still reports "unknown analyzer" first.
	repoRoot := deps.RootDir()
	if repoRootRequiringAnalyzers[a.Algorithm] {
		resolved, rerr := resolveRepoDir(ctx, deps, "topology/"+a.Algorithm, a.Repo)
		if rerr != nil {
			return errorResult(rerr.Error())
		}
		repoRoot = resolved
	}
	findings, err := analyzer.Run(ctx, foundation.Request{
		Caller:     gc,
		Graph:      kgtypes.GraphType(a.Graph),
		Name:       topologyInstanceName(a),
		RepoRoot:   repoRoot,
		PathPrefix: a.PathPrefix,
		TopK:       a.TopK,
		Language:   a.Language,
		Extra:      a.Extra,
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
//
// THE repo ARGUMENT SERVES TWO ROLES AND THEY DIVERGE FOR AN ABSOLUTE PATH. It
// names the code GRAPH instance here, and it is also the source of the walk ROOT
// above. Those coincide for a bare name; for `repo:"/Users/me/code/knowledge"`
// they do not, and passing the path through as the graph name would look up a
// graph keyed on a filesystem path. The basename is not an invention — it is the
// same rule that named the graph in the first place, the one collect records when
// it derives a code-graph name from the directory it collected.
func topologyInstanceName(a topologyArgs) string {
	switch {
	case a.Name != "":
		return a.Name
	case a.Repo != "":
		return codeGraphInstanceName(a.Repo)
	case a.Account != "":
		return a.Account
	default:
		return ""
	}
}

// codeGraphInstanceName resolves a repo ARGUMENT to the code-GRAPH instance name.
//
// The argument is overloaded: it names the graph AND, for the analyzers that walk
// a tree, it is the source of the walk root. Those coincide for a bare name and
// DIVERGE for an absolute path, where passing the path through would look up a
// graph keyed on a filesystem path. The basename is not an invention — it is the
// same rule that named the graph in the first place, the one collect records when
// it derives a code-graph name from the directory it collected.
//
// Shared by the topology dispatcher and manage_checks(run) so the two entry
// points into the same analyzer cannot attribute a scan to two different graphs.
func codeGraphInstanceName(repo string) string {
	if filepath.IsAbs(repo) {
		return filepath.Base(repo)
	}
	return repo
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
