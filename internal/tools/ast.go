// SPDX-License-Identifier: Apache-2.0

// ast.go — client-side intercept for the `ast` MCP tool. Source files live
// on the client's filesystem; the server has no repo (especially in
// Fulminate Cloud remote-server mode), so AST parsing runs locally here.
// Mirrors the existing collect / manage(status) intercept pattern wired
// from cmd/knowledge/mcp.go.
//
// There is exactly ONE ast schema, AstToolDef in ast_schema.go beside this
// file; the client's bootstrap augmentation advertises it to the LLM. The
// server never dispatches the tool, so there is no server-side mirror to keep
// in sync.
//
// File split: this file holds InterceptAst (entry point), the args struct,
// and the match/count/explain/list_node_kinds handlers. The search handler
// + helper utilities live in ast_handlers.go; resolving the `repo` argument to
// a walk directory lives in ast_repo_resolve.go.

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"

	"github.com/fulminate-io/knowledge-mcp/internal/ast"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// InterceptAst is the entry point invoked by mcp.go's intercept chain.
// Returns (true, result) when the call was handled; the caller forwards to
// the server only if this returns false. Mirrors InterceptCollect's shape.
//
// All five ops (match, count, replace, explain, list_node_kinds) run
// client-side. There is no fallthrough to the server: the server carries no
// dispatch arm for the ast tool at all, by design — it has no repo to parse.
func InterceptAst(ctx context.Context, deps ClientDeps, params kgtools.CallToolParams) (bool, kgtools.ToolResult) {
	if params.Name != "ast" {
		return false, kgtools.ToolResult{}
	}
	if err := rejectUndeclaredParams("ast", "", AstToolDef().InputSchema.Properties, params.Arguments); err != nil {
		return true, errorResult(err.Error())
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
		return true, unknownOperationResult("ast", a.Operation,
			[]string{"match", "count", "replace", "explain", "list_node_kinds"})
	}
}

// astArgs holds the parsed arguments for an ast tool call. Every json tag here
// MUST also be declared in AstToolDef's schema (ast_schema.go): the intercept
// rejects undeclared params up front, and the param-parity residue test asserts
// the two stay in step.
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
	// IncludeTests is a POINTER for presence, not for its value. The flag is
	// refused for a language that has no test-file convention — accepting it
	// there would be a control the caller believes is in force and is not — and
	// that refusal must fire only when the caller SUPPLIED the flag. A bare bool
	// cannot tell an omitted include_tests from an explicit false, so every
	// omission would error. Mirrors Replacement and DryRun below.
	IncludeTests *flexBool `json:"include_tests"`
	// LiftExclusions walks the files discovery's rule chain would decline
	// instead of declining them. The response says a run was lifted rather than
	// reporting zero exclusions, so the two stay distinguishable.
	LiftExclusions bool    `json:"lift_exclusions"`
	Limit          flexInt `json:"limit"`
	// Context pins the parse context the pattern compiles under. Empty is the
	// default and means the union of every context that hosts the pattern.
	Context string `json:"context"`
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

// defaultMatchRenderLimit bounds how many matches operation=match RENDERS when
// the caller supplies no limit. It is a response-size bound and nothing more:
// the walk behind it is always complete, and MatchResults.Total reports the
// full-walk count regardless of how many matches are rendered.
const defaultMatchRenderLimit = 100

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

	patterns, patternErrs, perr := buildAstPatterns(a)
	if perr != nil {
		return errorResult("parse pattern: " + perr.Error())
	}

	where, werr := ast.ParseWhere(a.Where)
	if werr != nil {
		return errorResult("parse where: " + werr.Error())
	}

	// A kind leaf naming a kind the grammar lacks can never match, so it is
	// answerable here rather than after a whole-scope walk. Refusing is what
	// keeps it distinguishable from a correct search that found nothing.
	if kerr := ast.ValidateWhereKinds(where, lang); kerr != nil {
		return errorResult(kerr.Error())
	}

	if perr := validateContextPin(a.Context, lang); perr != nil {
		return errorResult(perr.Error())
	}

	if terr := validateIncludeTests(a, lang); terr != nil {
		return errorResult(terr.Error())
	}

	repoDir, derr := resolveRepoDir(ctx, deps, a.Repo)
	if derr != nil {
		return errorResult(derr.Error())
	}

	scope := scopeFromArgs(a)
	raws, compiled, narrowed, walk, patternErrs, merr := matchAll(ctx, a, lang, patterns, patternErrs, where, repoDir, scope)
	if merr != nil {
		return errorResult("match: " + merr.Error())
	}

	// The walk above is complete; only the RENDER is bounded. Truncate BEFORE
	// Hydrate, never after: Hydrate builds a per-call code-node index over the
	// unique file set of the matches handed to it, so bounding its input keeps
	// that lookup proportional to what is actually returned. total is captured
	// first so the reply can disclose the full-walk count.
	total := len(raws)
	render := int(a.Limit)
	if render <= 0 {
		render = defaultMatchRenderLimit
	}
	raws = raws[:min(len(raws), render)]

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
	// generic no-match hint with the zero-scan one, which names whichever of the
	// three causes applies — scanned-but-no-match keeps the emptyResultHint
	// Hydrate already set.
	//
	// Compiled is set unconditionally, INCLUDING on a zero result: seeing which
	// construct the pattern became is what makes a wrong-context zero
	// diagnosable, and that is exactly the case with no matches to read it off.
	results.WalkedRoot = repoDir
	results.Total = total
	results.Compiled = compiled
	results.Narrowed = narrowed
	if walk.FilesScanned == 0 {
		results.Hint = ast.ZeroScanHint(repoDir, a.Language, scope, walk)
	}
	// pattern_errors rides alongside the results rather than replacing them: an
	// alternation member that could not be used is reported, and the members
	// that worked are still answered.
	return jsonResult(matchReply{MatchResults: results, PatternErrors: patternErrs})
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

	patterns, patternErrs, perr := buildAstPatterns(a)
	if perr != nil {
		return errorResult("parse pattern: " + perr.Error())
	}

	where, werr := ast.ParseWhere(a.Where)
	if werr != nil {
		return errorResult("parse where: " + werr.Error())
	}

	// A kind leaf naming a kind the grammar lacks can never match, so it is
	// answerable here rather than after a whole-scope walk. Refusing is what
	// keeps it distinguishable from a correct search that found nothing.
	if kerr := ast.ValidateWhereKinds(where, lang); kerr != nil {
		return errorResult(kerr.Error())
	}

	if perr := validateContextPin(a.Context, lang); perr != nil {
		return errorResult(perr.Error())
	}

	if terr := validateIncludeTests(a, lang); terr != nil {
		return errorResult(terr.Error())
	}

	repoDir, derr := resolveRepoDir(ctx, deps, a.Repo)
	if derr != nil {
		return errorResult(derr.Error())
	}

	scope := scopeFromArgs(a)
	// countAll runs the body-free walk (ast.Count) per pattern and merges the
	// tallies, so by_file and by_kind arrive pre-aggregated at O(files) instead
	// of being rebuilt here from a retained []RawMatch. by_kind splits the total
	// by the construct each match compiled to — a union's 12 fields + 28 locals
	// is a different answer from 40 of either — and a placeholder-rooted pattern
	// contributes an empty-string kind, which the tally preserves.
	tally, compiled, narrowed, walk, patternErrs, cerr := countAll(ctx, a, lang, patterns, patternErrs, where, repoDir, scope)
	if cerr != nil {
		return errorResult("count: " + cerr.Error())
	}

	res := map[string]any{
		"total":         tally.Total,
		"by_file":       tally.ByFile,
		"by_kind":       tally.ByKind,
		"compiled":      compiled,
		"narrowed":      narrowed,
		"walked_root":   repoDir,
		"files_scanned": walk.FilesScanned,
		"files_skipped": walk.FilesSkipped,
		"duration_ms":   walk.DurationMS,
		// files_skipped decomposed by cause, and the two degraded-parse
		// counters. count renders its walk metrics explicitly rather than
		// marshaling the struct, so these have to be listed here or the op
		// that exists to report totals would report the total without its
		// breakdown.
		"skipped_read":                walk.SkippedRead,
		"skipped_parse_error":         walk.SkippedParseError,
		"skipped_parse_limit":         walk.SkippedParseLimit,
		"files_with_parse_errors":     walk.FilesWithParseErrors,
		"matches_from_degraded_trees": walk.MatchesFromDegradedTrees,
		// count answers "how many are there", so what discovery never offered
		// the walk belongs in the same answer: a total is only readable
		// alongside the set it was computed over.
		"excluded_by_rule":   walk.ExcludedByRule,
		"excluded_samples":   walk.ExcludedSamples,
		"excluded_truncated": walk.ExcludedTruncated,
		"discovery_path":     walk.DiscoveryPath,
	}
	// count has no scanned-but-no-match hint, so its only hint is the zero-scan
	// one: fire it exactly when zero files were scanned.
	if walk.FilesScanned == 0 {
		res["hint"] = ast.ZeroScanHint(repoDir, a.Language, scope, walk)
	}
	// Only present when a member actually failed, so a clean call's shape is
	// exactly what it was before alternation could partially succeed.
	if len(patternErrs) > 0 {
		res["pattern_errors"] = patternErrs
	}
	return jsonResult(res)
}

// scopeFromArgs maps astArgs to ast.Scope. The scope narrows which files are
// walked and nothing else — the walk is never bounded. The `limit` argument is
// consumed only by handleAstMatch, as a bound on how many matches are
// RENDERED; count and replace share this scope and so cannot see it at all.
func scopeFromArgs(a astArgs) ast.Scope {
	return ast.Scope{
		Repo:            a.Repo,
		PackagePrefixes: a.PackagePrefixes,
		IncludeTests:    a.IncludeTests != nil && bool(*a.IncludeTests),
		LiftExclusions:  a.LiftExclusions,
	}
}

// validateIncludeTests refuses include_tests for a language ast carries no
// test-file convention for. The alternative is the shape this whole surface
// exists to remove: a documented filter that is accepted and then does nothing,
// which on a replace means a blast-radius control the caller believes is holding
// while the walk rewrites test files anyway.
//
// The asymmetry is deliberate and is why astArgs.IncludeTests is a pointer: an
// OMITTED flag is never an error, because the schema default is false and a
// caller who never asked for the control was never misled about it.
func validateIncludeTests(a astArgs, lang treesitter.Language) error {
	if a.IncludeTests == nil || ast.HasTestFilePredicate(lang) {
		return nil
	}
	supported := ast.TestFilePredicateLanguages()
	return fmt.Errorf(
		"include_tests is not supported for language %s: ast has no test-file convention registered for it, so the flag would silently do nothing. Languages that do: %s. Omit include_tests to walk every file, tests included, and narrow with package_prefixes instead",
		lang, strings.Join(supported, ", "))
}
