// SPDX-License-Identifier: Apache-2.0

// ast.go — client-side intercept for the `ast` MCP tool. Source files live
// on the client's filesystem; the server has no repo (especially in
// Fulminate Cloud remote-server mode), so AST parsing runs locally here.
// Mirrors the existing collect / manage(status) intercept pattern wired
// from cmd/knowledge/mcp.go.
//
// The server-side schema lives at cmd/knowledge-server/tools/
// tools_ast.go::AstToolDef so tools/list still advertises the tool — the
// server-side handler errors out with "client-side intercept required" if
// a non-stdio caller forwards the call directly.
//
// File split: this file holds InterceptAst (entry point), the args struct,
// and the match/count/explain/list_node_kinds handlers. The search handler
// + helper utilities live in ast_handlers.go.

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"

	"github.com/fulminate-io/knowledge-mcp/internal/ast"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// InterceptAst is the entry point invoked by mcp.go's intercept chain.
// Returns (true, result) when the call was handled; the caller forwards to
// the server only if this returns false. Mirrors InterceptCollect's shape.
//
// All five ops (match, search, count, explain, list_node_kinds) run
// client-side. There is no fallthrough to the server — the server-side
// dispatch errors out for the ast tool by design.
func InterceptAst(deps ClientDeps, params kgtools.CallToolParams) (bool, kgtools.ToolResult) {
	if params.Name != "ast" {
		return false, kgtools.ToolResult{}
	}
	var a astArgs
	if err := json.Unmarshal(params.Arguments, &a); err != nil {
		return true, errorResult("invalid arguments: " + err.Error())
	}
	ctx := context.Background()
	switch a.Operation {
	case "match":
		return true, handleAstMatch(ctx, deps, a)
	case "count":
		return true, handleAstCount(ctx, deps, a)
	case "replace":
		return true, handleAstReplace(ctx, deps, a)
	case "explain":
		return true, handleAstExplain(ctx, a)
	case "list_node_kinds":
		return true, handleAstListNodeKinds(a)
	default:
		return true, errorResult("unknown operation: " + a.Operation + " — use match, count, replace, explain, or list_node_kinds")
	}
}

// astArgs holds the parsed arguments for an ast tool call. The wire shape
// matches the server-side AstToolDef schema in
// cmd/knowledge-server/tools/tools_ast.go.
//
// Where is left as json.RawMessage so the args boundary does not depend on
// the ast engine types — the handler hands the raw bytes to ast.ParseWhere
// and owns where-tree parsing entirely inside the engine package.
type astArgs struct {
	Operation       string          `json:"operation"`
	Language        string          `json:"language"`
	Pattern         string          `json:"pattern"`
	Patterns        []string        `json:"patterns,omitempty"`
	Where           json.RawMessage `json:"where"`
	Snippet         string          `json:"snippet"`
	Repo            string          `json:"repo"`
	PackagePrefixes []string        `json:"package_prefixes"`
	IncludeTests    bool            `json:"include_tests"`
	Limit           flexInt         `json:"limit"`
	// Replacement is the operation=replace template in the $X DSL grammar.
	Replacement string `json:"replacement"`
	// DryRun is a POINTER so an absent dry_run defaults to TRUE (preview)
	// while an explicit dry_run:false is honored as apply. A bare flexBool
	// would default to false and invert the ticket's default.
	DryRun *flexBool `json:"dry_run"`
}

// handleAstMatch parses the DSL pattern (or each pattern in `patterns`),
// parses the JSON where-tree (when present), runs the engine walker, and
// returns the hydrated MatchResults. When `patterns` is set, results are
// unioned across patterns — used for sibling-form rules (e.g., a single
// annotation covering both `log.Print($$$_)` and `log.Println($$$_)`).
//
// Hydration runs against ast.NoOpBackend on the client: the client doesn't
// carry the code graph in-process, so EnclosingNodeID + EnclosingSignature
// are always empty in the returned matches. The LLM can issue a separate
// query() call against the code graph if it needs enclosing-node IDs.
func handleAstMatch(ctx context.Context, deps ClientDeps, a astArgs) kgtools.ToolResult {
	lang := treesitter.Language(a.Language)
	if _, ok := treesitter.LanguageGrammar(lang); !ok {
		return errorResult("unsupported language: " + a.Language)
	}

	patterns, perr := buildAstPatterns(a)
	if perr != nil {
		return errorResult("parse pattern: " + perr.Error())
	}

	where, werr := ast.ParseWhere(a.Where)
	if werr != nil {
		return errorResult("parse where: " + werr.Error())
	}

	repoDir, derr := resolveRepoDir(deps)
	if derr != nil {
		return errorResult(derr.Error())
	}

	scope := scopeFromArgs(a)
	raws, walk, merr := matchAll(ctx, lang, patterns, where, repoDir, scope)
	if merr != nil {
		return errorResult("match: " + merr.Error())
	}

	// Default the hydration repo from the cwd resolver when the caller did not
	// pass one, so match hydration is correct without the model supplying repo.
	a.Repo = defaultRepoFromCwd(ctx, deps, a.Repo)

	// nil GraphCaller is tolerated — the hydrator's b.gc == nil branch returns
	// empty hydration without error, matching the prior behavior where
	// the bare GraphClient could legitimately be nil in test harnesses.
	results, herr := ast.Hydrate(ctx, graphClientHydratorBackend{gc: deps.GraphCaller(), repo: a.Repo}, raws, walk)
	if herr != nil {
		return errorResult("hydrate: " + herr.Error())
	}
	return jsonResult(results)
}

// defaultRepoFromCwd returns explicit when it is non-empty; otherwise it
// resolves the repo from the cwd resolver and returns the resolved name on a
// hit, or "" on a miss/error. Hit-only by design: ast hydration must NOT fail
// when the cwd matches no loaded code graph — a missing repo only degrades
// hydration quality (count/replace don't hydrate at all, and the hydrator
// tolerates an empty repo). This is why the soft variant is inlined here rather
// than reusing resolveTopologyRepo, which errors on a miss.
func defaultRepoFromCwd(ctx context.Context, deps ClientDeps, explicit string) string {
	if explicit != "" {
		return explicit
	}
	resolver := deps.RepoResolver()
	if resolver == nil {
		return ""
	}
	if name, ok, err := resolver.ResolveCwd(ctx, deps.RootDir()); err == nil && ok {
		return name
	}
	return ""
}

// handleAstCount runs the same walk as match but skips Hydrate. Returns a
// JSON shape with total, by_file (repo-relative path keys, identical to
// RawMatch.FilePath semantics — no absolute-path leakage), and walk metrics.
// Honors `patterns` (sibling-form alternation) the same way as handleAstMatch.
func handleAstCount(ctx context.Context, deps ClientDeps, a astArgs) kgtools.ToolResult {
	lang := treesitter.Language(a.Language)
	if _, ok := treesitter.LanguageGrammar(lang); !ok {
		return errorResult("unsupported language: " + a.Language)
	}

	patterns, perr := buildAstPatterns(a)
	if perr != nil {
		return errorResult("parse pattern: " + perr.Error())
	}

	where, werr := ast.ParseWhere(a.Where)
	if werr != nil {
		return errorResult("parse where: " + werr.Error())
	}

	repoDir, derr := resolveRepoDir(deps)
	if derr != nil {
		return errorResult(derr.Error())
	}

	scope := scopeFromArgs(a)
	raws, walk, merr := matchAll(ctx, lang, patterns, where, repoDir, scope)
	if merr != nil {
		return errorResult("match: " + merr.Error())
	}

	byFile := make(map[string]int, len(raws))
	for _, r := range raws {
		byFile[r.FilePath]++
	}
	return jsonResult(map[string]any{
		"total":         len(raws),
		"by_file":       byFile,
		"files_scanned": walk.FilesScanned,
		"files_skipped": walk.FilesSkipped,
		"duration_ms":   walk.DurationMS,
	})
}

// buildAstPatterns parses one or more DSL patterns from the args.
// Mutually-exclusive: pattern OR patterns, never both. Returns at least
// one ast.Pattern on success; an actionable error when neither is set.
func buildAstPatterns(a astArgs) ([]ast.Pattern, error) {
	hasSingle := strings.TrimSpace(a.Pattern) != ""
	if hasSingle && len(a.Patterns) > 0 {
		return nil, fmt.Errorf("specify pattern OR patterns, not both")
	}
	if !hasSingle && len(a.Patterns) == 0 {
		return nil, fmt.Errorf("operation=%s requires pattern (or patterns for sibling-form alternation)", a.Operation)
	}
	if hasSingle {
		p, err := ast.Parse(a.Pattern)
		if err != nil {
			return nil, err
		}
		return []ast.Pattern{p}, nil
	}
	out := make([]ast.Pattern, 0, len(a.Patterns))
	for i, src := range a.Patterns {
		s := strings.TrimSpace(src)
		if s == "" {
			return nil, fmt.Errorf("patterns[%d] is empty", i)
		}
		p, err := ast.Parse(s)
		if err != nil {
			return nil, fmt.Errorf("patterns[%d] %q: %w", i, s, err)
		}
		out = append(out, p)
	}
	return out, nil
}

// matchAll compiles each pattern and runs ast.Match, unioning the results.
// Walk stats are summed: FilesScanned/FilesSkipped take the max across runs
// (same repo, single-pass scans), DurationMS sums (sequential walks).
//
// Perf note: each pattern triggers an independent repo walk. For a worker
// running N corpus annotations × M sibling forms each, this is N×M walks.
// The MVP shape is acceptable for the current corpus size; if the worker's
// total wall-clock becomes a concern, the engine can be extended to compile
// all patterns up front and dispatch matchTree per node per pattern in a
// single walk (constant-N file IO).
func matchAll(ctx context.Context, lang treesitter.Language, patterns []ast.Pattern, where *ast.WhereNode, repoDir string, scope ast.Scope) ([]ast.RawMatch, ast.WalkStats, error) {
	var union []ast.RawMatch
	var walk ast.WalkStats
	for _, pat := range patterns {
		cp, err := ast.Compile(pat, lang)
		if err != nil {
			return nil, ast.WalkStats{}, fmt.Errorf("compile pattern %q: %w", pat.Source, err)
		}
		raws, w, merr := ast.Match(ctx, repoDir, lang, cp, where, scope)
		cp.Close()
		if merr != nil {
			return nil, ast.WalkStats{}, merr
		}
		union = append(union, raws...)
		if w.FilesScanned > walk.FilesScanned {
			walk.FilesScanned = w.FilesScanned
		}
		if w.FilesSkipped > walk.FilesSkipped {
			walk.FilesSkipped = w.FilesSkipped
		}
		walk.DurationMS += w.DurationMS
	}
	return union, walk, nil
}

// scopeFromArgs maps astArgs to ast.Scope. Limit defaults are owned by
// ast.Match (defaultLimit when scope.Limit <= 0).
func scopeFromArgs(a astArgs) ast.Scope {
	return ast.Scope{
		Repo:            a.Repo,
		PackagePrefixes: a.PackagePrefixes,
		IncludeTests:    a.IncludeTests,
		Limit:           int(a.Limit),
	}
}

// resolveRepoDir returns the directory the AST walk should run over. The
// client uses --root verbatim (the user passes the repo path when launching
// knowledge). No SyncRootKey lookup needed — the client owns the filesystem
// boundary; if --root points at the wrong place that's a launch-flag issue,
// not something we resolve via the code graph.
func resolveRepoDir(deps ClientDeps) (string, error) {
	dir := strings.TrimSpace(deps.RootDir())
	if dir == "" {
		return "", fmt.Errorf("ast: --root is empty; pass a repo path with --root <dir>")
	}
	return dir, nil
}
