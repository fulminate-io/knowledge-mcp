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
	"os"
	"path/filepath"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"

	"github.com/fulminate-io/knowledge-mcp/internal/ast"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
	"github.com/fulminate-io/knowledge-mcp/internal/session"
)

// InterceptAst is the entry point invoked by mcp.go's intercept chain.
// Returns (true, result) when the call was handled; the caller forwards to
// the server only if this returns false. Mirrors InterceptCollect's shape.
//
// All five ops (match, search, count, explain, list_node_kinds) run
// client-side. There is no fallthrough to the server — the server-side
// dispatch errors out for the ast tool by design.
func InterceptAst(ctx context.Context, deps ClientDeps, params kgtools.CallToolParams) (bool, kgtools.ToolResult) {
	if params.Name != "ast" {
		return false, kgtools.ToolResult{}
	}
	var a astArgs
	if err := json.Unmarshal(params.Arguments, &a); err != nil {
		return true, errorResult("invalid arguments: " + err.Error())
	}
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
	// Replacement is the operation=replace template in the $X DSL grammar. It is
	// a POINTER so an ABSENT replacement (the arg was omitted) is distinguishable
	// from an EXPLICIT empty string: nil → "requires replacement" error; "" →
	// DELETE the matched ranges (the template interpolates to "", splicing
	// nothing). Mirrors the DryRun pointer-for-presence below.
	Replacement *string `json:"replacement"`
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

	repoDir, derr := resolveRepoDir(ctx, deps, a.Repo)
	if derr != nil {
		return errorResult(derr.Error())
	}

	scope := scopeFromArgs(a)
	raws, walk, merr := matchAll(ctx, lang, patterns, where, repoDir, scope)
	if merr != nil {
		return errorResult("match: " + merr.Error())
	}

	// Hydration looks up the enclosing-function node in the code graph by NAME —
	// and that name is the walk-root's basename, exactly how `collect` keys the
	// graph. Derive it from the resolved directory, never from a cwd→catalog
	// guess (empty / abs path / current tree all resolve to a real dir above).
	a.Repo = filepath.Base(repoDir)

	// Hydration reads the branch overlay the same way file_symbols does. Derived
	// here because ast carries no branch arg: autoDetectBranch resolves the repo's
	// recorded on-disk directory from the manifest and reads its current branch,
	// keyed on the same graph name hydration uses. It returns "" on a manifest
	// miss or detached HEAD, which falls through to the base graph as before.
	branch := autoDetectBranch(ctx, a.Repo)

	// nil GraphCaller is tolerated — the hydrator's b.gc == nil branch returns
	// empty hydration without error, matching the prior behavior where
	// the bare GraphClient could legitimately be nil in test harnesses.
	results, herr := ast.Hydrate(ctx, graphClientHydratorBackend{gc: deps.GraphCaller(), repo: a.Repo, branch: branch}, raws, walk)
	if herr != nil {
		return errorResult("hydrate: " + herr.Error())
	}
	// Echo the directory actually walked so the caller can tell which tree
	// produced the matches. When NOTHING was scanned (zero files), override the
	// generic no-match hint with the wrong-root hint — scanned-but-no-match keeps
	// the emptyResultHint Hydrate already set.
	results.WalkedRoot = repoDir
	if walk.FilesScanned == 0 {
		results.Hint = ast.ZeroScanHint(repoDir, a.Language)
	}
	return jsonResult(results)
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

	repoDir, derr := resolveRepoDir(ctx, deps, a.Repo)
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
	res := map[string]any{
		"total":         len(raws),
		"by_file":       byFile,
		"walked_root":   repoDir,
		"files_scanned": walk.FilesScanned,
		"files_skipped": walk.FilesSkipped,
		"duration_ms":   walk.DurationMS,
	}
	// count has no scanned-but-no-match hint, so its only hint is the wrong-root
	// one: fire it exactly when zero files were scanned.
	if walk.FilesScanned == 0 {
		res["hint"] = ast.ZeroScanHint(repoDir, a.Language)
	}
	return jsonResult(res)
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

// rootDirSourcer is the OPTIONAL deps capability exposing whether the daemon's
// --root was explicitly set (vs the built-in "." default). Type-asserted rather
// than added to ClientDeps so the many test fakes that never set a root are
// unaffected; the production *client implements it over Config.RootDirSet.
type rootDirSourcer interface{ RootDirSet() bool }

// resolveRepoDir returns the directory the AST walk should run over, honoring
// the repo arg so an ast call from a session rooted at repo Y can target a
// named repo X that is checked out as a sibling directory.
//
// Base is effectiveCwd(ctx, deps): the per-session workspace cwd carried on ctx
// (HTTP transport) when present, else deps.RootDir() (the process --root, the
// stdio default). The session cwd rides in on the chain ctx that InterceptAst
// now threads through, so an HTTP ast call walks the caller's session tree while
// a stdio call walks --root.
//
// Resolution:
//   - empty base → typed "--root is empty" error (unchanged contract).
//   - repoArg == "" → walk base (the current tree) — but fail loud FIRST when
//     there is no session cwd AND --root was left at its "." default, so a
//     rootless daemon does not silently walk its own process cwd.
//   - repoArg is an ABSOLUTE PATH → an explicit directory IS the user's
//     instruction: when it stats as a directory, walk it directly (no sibling
//     probe, no ResolveCwd gate — it can target ANY local checkout, not just a
//     monorepo sibling); when it does not exist (or is not a directory), the
//     FAIL-LOUD typed error fires. This branch runs BEFORE the name-based logic.
//   - repoArg names the SAME repo as base's cwd (its basename / a path component
//     of base) → return base.
//   - repoArg names a DIFFERENT repo recorded in the machine-local manifest
//     (~/.knowledge/repos.json, populated at collect time) → return that recorded
//     directory when it still stats as a dir. This is a recorded fact, not a
//     guess: the manifest stores where the repo was actually collected from on
//     THIS machine.
//   - otherwise → typed error (the FAIL-LOUD floor). We NEVER silently fall back
//     to base for a cross-repo arg: returning base would walk the WRONG tree and
//     hand back results labeled for repoArg, the exact bug this guards against.
//
// The cross-repo path resolves ONLY via the manifest (a collect-time recorded
// path) — never a sibling-dir / git-remote / content guess. A repo name is not a
// portable filesystem path, so absent a manifest entry the fail-loud floor directs
// the caller to an absolute checkout path.
func resolveRepoDir(ctx context.Context, deps ClientDeps, repoArg string) (string, error) {
	base := strings.TrimSpace(effectiveCwd(ctx, deps))
	if base == "" {
		return "", fmt.Errorf("ast: --root is empty; pass a repo path with --root <dir>")
	}
	// Anchor a RELATIVE base to an absolute path. effectiveCwd can hand back a
	// relative root — notably the daemon's default --root of "." when no session
	// WorkspaceCwd is propagated over the HTTP transport. The walker tolerates a
	// relative root (it resolves against the daemon's process cwd), but the
	// sibling probe (filepath.Dir) and the current-repo path match below need a
	// real absolute tree: filepath.Dir(".") is "." with no parent, which is why a
	// cross-repo arg could never find a sibling. filepath.Abs anchors "." to the
	// daemon's process cwd (the repo it was launched in).
	if absBase, absErr := filepath.Abs(base); absErr == nil {
		base = absBase
	}
	repoArg = strings.TrimSpace(repoArg)
	if repoArg == "" {
		// Fail loud when the walk root is a pure default: no session cwd rode in
		// over the transport AND --root was left at its "." built-in default. In
		// that case `base` is just the daemon's process cwd, which almost never
		// is the tree the caller means — walking it silently hands back results
		// labeled for the wrong repo. Two inputs are read SEPARATELY: an explicit
		// --root OR a live session cwd each preserve the walk. A deps that does
		// not expose RootDirSet() (older/partial fakes) keeps the fallback.
		if session.WorkspaceCwdFromContext(ctx) == "" {
			if rs, ok := deps.(rootDirSourcer); ok && !rs.RootDirSet() {
				return "", fmt.Errorf("ast: no repo specified and the daemon has no project root — pass repo:<name|/abs/path> or start the daemon with --root <dir>")
			}
		}
		return base, nil
	}

	// Absolute path: an explicit directory is the user's direct instruction —
	// walk it as-is when it exists, with no sibling probe. This lets ast target
	// ANY local checkout, not just a monorepo sibling. A non-existent /
	// non-directory path hits the fail-loud floor below.
	if filepath.IsAbs(repoArg) {
		if info, statErr := os.Stat(repoArg); statErr == nil && info.IsDir() {
			return repoArg, nil
		}
		return "", fmt.Errorf("ast: repo %q is an absolute path but not an existing directory; pass an existing checkout directory, or omit repo to walk the current tree", repoArg)
	}

	// A bare NAME resolves ONLY when it names the CURRENT tree — the daemon's
	// rooted repo, whose absolute path we know first-hand from `base`. It matches
	// when repoArg is that tree's basename, or appears as a path component (the
	// cwd is a subdir of the repo). This reads the real directory; it is NOT a
	// name→path guess.
	if repoArg == filepath.Base(base) ||
		strings.HasSuffix(base, "/"+repoArg) ||
		strings.Contains(base, "/"+repoArg+"/") {
		return base, nil
	}

	// Cross-repo NAME → machine-local manifest. The ~/.knowledge/repos.json
	// manifest records, at collect time, where each repo was actually collected
	// from on THIS machine (repo name → absolute path). This is a RECORDED FACT,
	// not a portability-breaking guess: when the named repo has been collected
	// here, we know its real directory first-hand. Stat-gate it so a manifest
	// entry whose checkout has since moved/been deleted falls through to the
	// fail-loud floor rather than walking a phantom path.
	if dir, ok := lookupRepoDir(repoArg); ok {
		if info, statErr := os.Stat(dir); statErr == nil && info.IsDir() {
			return dir, nil
		}
	}

	// FAIL-LOUD: the arg names neither the current tree, an absolute path, nor a
	// repo recorded in the machine-local manifest. We deliberately do NOT GUESS a
	// directory from the name (e.g. a sibling probe): a repo name is not a portable
	// filesystem path — it lives at a different location on every machine. The
	// manifest above is the only name→dir source, because it stores the actual
	// collect-time path; absent a manifest entry, an absolute checkout path is the
	// reliable cross-repo target.
	return "", fmt.Errorf("ast: repo %q is not the current tree and is not in the local manifest (~/.knowledge/repos.json — populated when you `collect` a repo). Collect it first, register its path with manage(operation:\"register_repo\", name:%q, root:\"/abs/path\"), pass an absolute checkout path, e.g. repo=\"/path/to/%s\", or omit repo to walk the current tree", repoArg, repoArg, repoArg)
}
