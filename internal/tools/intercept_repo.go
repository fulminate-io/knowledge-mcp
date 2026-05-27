// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/coderun"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// codeGraphToolNames is the allowlist of MCP tool names that target a
// specific code graph and therefore require repo: + branch: server-side.
// Other tools — knowledge graph queries, log graph reads, manage —
// are not touched by InjectRepoIfCodeGraph except via the dedicated
// manage subdispatch path below.
//
// Note on query / traverse: both accept a `graph` selector and only act
// on the code graph when graph is "code" or unset. The graph-selector
// gate inside InjectRepoIfCodeGraph filters these — see step 2.
//
// manage is NOT in this map — its code-graph-targeted operations are
// handled separately in injectManageRepo because they use `name` as the
// repo identifier (overloaded across non-code uses) rather than the
// dedicated `repo:` field this map's entries share.
var codeGraphToolNames = map[string]bool{
	"search":       true,
	"file_symbols": true,
	"query":        true,
	"traverse":     true,
}

// manageCodeGraphOps is the set of manage operations that target a
// specific code graph and therefore need the repo (passed via `name:`)
// injected from cwd. Branch is NEVER auto-filled here:
//   - delete_branch: the branch arg names the overlay to delete; auto-
//     filling current branch would delete the wrong overlay.
//   - list_branches: branch isn't part of the call.
//   - rebuild_hnsw / rebuild_bm25: branchless (rebuild applies to the
//     base graph; per-branch overlays inherit BM25/HNSW via merge).
//
// rebuild_hnsw and rebuild_bm25 also accept practice / cloud / cicd
// graph types where `name` carries a different identifier (account /
// graph-id). Inject only when graph=="code".
var manageCodeGraphOps = map[string]bool{
	"list_branches": true,
	"delete_branch": true,
	"rebuild_hnsw":  true,
	"rebuild_bm25":  true,
}

// InjectRepoIfCodeGraph rewrites a code-graph-targeted CallToolParams to
// carry repo: + branch: (and, for search with staleness:true, the three
// git-state fields current_head / uncommitted_count / commits_behind).
//
// FUL-241 Phase 4: the server is filesystem-blind. Every code-graph
// tool call must arrive with explicit repo: AND branch:. This intercept
// is the canonical client-side filler that walks cwd → loaded-graph
// name (via deps.RepoResolver) and shells out to coderun helpers for
// branch / git state. When the cwd doesn't match a loaded code graph
// AND the caller did not pass repo: explicitly, the call short-circuits
// with a typed error WITHOUT issuing the wire RPC. Same posture for
// missing branch on a non-git cwd.
//
// Return tuple:
//   - rewritten: a possibly-mutated CallToolParams. When no rewrite was
//     necessary, the original params is returned unchanged.
//   - handled: true ONLY in the error short-circuit case. Callers MUST
//     forward res to the user and skip the rest of the intercept chain.
//   - res: the error ToolResult when handled is true; zero value
//     otherwise.
//
// Behavior:
//
//  1. Tool-name allowlist: only acts on tools in codeGraphToolNames.
//     Other tools return (params, false, _).
//
//  2. Graph-selector gate (for query / traverse): only proceed when
//     args["graph"] is missing OR equals "code". Knowledge / cloud /
//     cicd / practice / linkage variants pass through.
//
//  3. Repo injection: missing/empty args["repo"] → ResolveCwd. Hit
//     populates args["repo"]; miss returns typed error.
//
//  4. Branch injection: missing/empty args["branch"] → coderun.DetectBranch.
//     Success populates args["branch"]; error or empty result returns
//     typed error.
//
//  5. Staleness trio: ONLY when tool == "search" AND args["staleness"]
//     is true. Populates current_head / uncommitted_count /
//     commits_behind from coderun helpers. Skips all three subprocess
//     calls when staleness is unset/false.
//
//  6. Explicit-value preservation: caller-supplied values are never
//     overwritten. Resolver / detect / staleness helpers only fill
//     MISSING fields.
func InjectRepoIfCodeGraph(ctx context.Context, deps ClientDeps, params kgtools.CallToolParams) (kgtools.CallToolParams, bool, kgtools.ToolResult) {
	// manage subdispatch: code-graph-targeted operations (list_branches,
	// delete_branch, rebuild_{hnsw,bm25} with graph=code) carry the repo
	// as `name:` rather than `repo:`. Branch is never auto-filled.
	if params.Name == "manage" {
		return injectManageRepo(ctx, deps, params)
	}

	if !codeGraphToolNames[params.Name] {
		return params, false, kgtools.ToolResult{}
	}

	args := decodeRepoArgs(params.Arguments)

	// Graph-selector gate: query / traverse are knowledge-graph by default.
	// Only proceed when graph is empty (code-graph default for code-only
	// tools is enforced server-side) or explicitly "code".
	if params.Name == "query" || params.Name == "traverse" {
		graph := decodeStringField(args, "graph")
		if graph != "" && graph != "code" {
			return params, false, kgtools.ToolResult{}
		}
	}

	cwd := deps.RootDir()

	// Step 3: Repo injection.
	if decodeStringField(args, "repo") == "" {
		repo, ok, err := resolveRepo(ctx, deps, cwd)
		if err != nil {
			return params, true, errorResult(params.Name + ": resolve repo: " + err.Error())
		}
		if !ok {
			return params, true, errorResult(params.Name + ": repo is required; run from inside an indexed code repo or pass repo:")
		}
		setStringField(args, "repo", repo)
	}

	// Step 4: Branch injection.
	if decodeStringField(args, "branch") == "" {
		branch, err := coderun.DetectBranch(ctx, cwd)
		if err != nil {
			return params, true, errorResult(params.Name + ": branch is required; run from inside a git working tree or pass branch: (" + err.Error() + ")")
		}
		if branch == "" {
			return params, true, errorResult(params.Name + ": branch is required; run from inside a git working tree or pass branch:")
		}
		setStringField(args, "branch", branch)
	}

	// Step 5: Staleness trio (search only, staleness:true only).
	if params.Name == "search" && decodeBoolField(args, "staleness") {
		populateStaleness(ctx, args, cwd)
	}

	rewritten, err := json.Marshal(args)
	if err != nil {
		return params, true, errorResult(params.Name + ": re-encode args: " + err.Error())
	}
	params.Arguments = rewritten
	return params, false, kgtools.ToolResult{}
}

// resolveRepo wraps the deps.RepoResolver call with a nil-resolver guard.
// Returns ("", false, nil) when no resolver is wired (test harnesses
// that don't exercise repo injection) so the caller surfaces the
// "repo is required" error rather than a misleading nil deref.
func resolveRepo(ctx context.Context, deps ClientDeps, cwd string) (string, bool, error) {
	r := deps.RepoResolver()
	if r == nil {
		return "", false, nil
	}
	return r.ResolveCwd(ctx, cwd)
}

// populateStaleness fills current_head / uncommitted_count / commits_behind
// from coderun helpers when the staleness opt-in is set. Each helper
// degrades gracefully (returns "" / 0 on non-git cwd) so missing git
// state never blocks a search — only the rendered staleness line
// degrades. The sync_commit input for CommitsBehind is unknown to the
// client; we pass "" and accept that commits_behind reports 0 in
// practice. The server's StalenessInfoWith would have made this same
// rev-list call before FUL-241 Phase 3, so behavior is preserved
// (server now relies on whatever the client passes).
func populateStaleness(ctx context.Context, args map[string]json.RawMessage, cwd string) {
	if _, alreadySet := args["current_head"]; !alreadySet {
		if sha, err := coderun.HeadCommit(ctx, cwd); err == nil && sha != "" {
			if b, mErr := json.Marshal(sha); mErr == nil {
				args["current_head"] = b
			}
		}
	}
	if _, alreadySet := args["uncommitted_count"]; !alreadySet {
		if n, err := coderun.UncommittedCount(ctx, cwd); err == nil {
			if b, mErr := json.Marshal(n); mErr == nil {
				args["uncommitted_count"] = b
			}
		}
	}
	if _, alreadySet := args["commits_behind"]; !alreadySet {
		// sync_commit isn't visible client-side; server's
		// StalenessInfoWith degrades gracefully when commits_behind is 0.
		if n, err := coderun.CommitsBehind(ctx, cwd, ""); err == nil {
			if b, mErr := json.Marshal(n); mErr == nil {
				args["commits_behind"] = b
			}
		}
	}
}

// decodeRepoArgs unmarshals params.Arguments into a per-key map. Returns
// an empty map on decode failure so the caller still gets a usable
// destination for setStringField — downstream re-marshal will succeed
// against the populated map even if the original payload was malformed.
func decodeRepoArgs(raw json.RawMessage) map[string]json.RawMessage {
	args := map[string]json.RawMessage{}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &args)
	}
	return args
}

// decodeStringField returns the string value of a known field, or "" if
// the field is missing / wrong type / empty.
func decodeStringField(args map[string]json.RawMessage, key string) string {
	raw, ok := args[key]
	if !ok {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	return s
}

// decodeBoolField returns the bool value of a known field, or false
// when missing / wrong type. Used for the staleness opt-in gate.
func decodeBoolField(args map[string]json.RawMessage, key string) bool {
	raw, ok := args[key]
	if !ok {
		return false
	}
	var b bool
	if err := json.Unmarshal(raw, &b); err != nil {
		return false
	}
	return b
}

// setStringField sets args[key] to the JSON-encoded string value.
func setStringField(args map[string]json.RawMessage, key, value string) {
	b, err := json.Marshal(value)
	if err != nil {
		return
	}
	args[key] = b
}

// injectManageRepo handles the manage tool's code-graph subdispatch.
// list_branches / delete_branch always need the repo (carried in `name:`);
// rebuild_hnsw / rebuild_bm25 need it only when graph=="code". For other
// manage operations (status, pprof_*, log ops, set_metadata_overrides,
// promote_metadata, clear_llm_failures, topology, link) this is a no-op
// fall-through. Branch is never auto-filled — delete_branch's branch
// names the overlay to remove, not the current checkout.
//
// Returns the same tuple shape as InjectRepoIfCodeGraph: rewritten
// params on continue, (params, true, errResult) on a typed short-circuit
// (cwd doesn't match a loaded code graph for a code-targeted op).
func injectManageRepo(ctx context.Context, deps ClientDeps, params kgtools.CallToolParams) (kgtools.CallToolParams, bool, kgtools.ToolResult) {
	args := decodeRepoArgs(params.Arguments)

	op := decodeStringField(args, "operation")
	if !manageCodeGraphOps[op] {
		return params, false, kgtools.ToolResult{}
	}

	// rebuild_hnsw / rebuild_bm25 are multi-graph — only the code graph
	// route uses cwd-derived repo. practice / cloud / cicd / knowledge
	// pass through untouched.
	if op == "rebuild_hnsw" || op == "rebuild_bm25" {
		if decodeStringField(args, "graph") != "code" {
			return params, false, kgtools.ToolResult{}
		}
	}

	if decodeStringField(args, "name") != "" {
		return params, false, kgtools.ToolResult{}
	}

	repo, ok, err := resolveRepo(ctx, deps, deps.RootDir())
	if err != nil {
		return params, true, errorResult("manage(" + op + "): resolve repo: " + err.Error())
	}
	if !ok {
		return params, true, errorResult("manage(" + op + "): repo is required; run from inside an indexed code repo or pass name:")
	}
	setStringField(args, "name", repo)

	rewritten, err := json.Marshal(args)
	if err != nil {
		return params, true, errorResult("manage(" + op + "): re-encode args: " + err.Error())
	}
	params.Arguments = rewritten
	return params, false, kgtools.ToolResult{}
}
