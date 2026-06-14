// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/coderun"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/session"
)

// effectiveCwd is the cwd repo resolution uses: the per-session workspace cwd
// carried on ctx (HTTP transport, set by the daemon from peer-cwd resolution)
// when present, else the process-global --root (deps.RootDir(), the stdio
// default). This is what lets two concurrent HTTP sessions from different
// repos each resolve their own code graph: the stdio path carries no workspace
// cwd, so it falls back to --root exactly as before.
func effectiveCwd(ctx context.Context, deps ClientDeps) string {
	if cwd := session.WorkspaceCwdFromContext(ctx); cwd != "" {
		return cwd
	}
	return deps.RootDir()
}

// codeGraphToolNames is the allowlist of MCP tool names that target a
// specific code graph and therefore require repo: + branch: server-side.
// Other tools — knowledge graph queries, log graph reads, manage —
// are not touched by InjectRepoIfCodeGraph except via the dedicated
// manage subdispatch path below.
//
// Note on search / query / traverse: these accept a `graph` selector and
// only act on the code graph when graph is "code" or unset. The graph-selector
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
//
// (rebuild_hnsw / rebuild_bm25 were retired with the server search
// subsystem.)
var manageCodeGraphOps = map[string]bool{
	"list_branches": true,
	"delete_branch": true,
}

// InjectRepoIfCodeGraph rewrites a code-graph-targeted CallToolParams to
// carry repo: + branch: (and, for search with staleness:true, the three
// git-state fields current_head / uncommitted_count / commits_behind).
//
// Phase 4: the server is filesystem-blind. Every code-graph
// tool call must arrive with explicit repo: AND branch:. This intercept
// is the canonical client-side filler that walks cwd → loaded-graph
// name (via deps.RepoResolver) and shells out to coderun helpers for
// branch / git state. The cwd is the per-session workspace cwd carried on
// ctx (HTTP transport, from peer-cwd resolution) when present, else the
// process --root (deps.RootDir(), the stdio default) — see effectiveCwd.
// When the cwd doesn't match a loaded code graph
// AND the caller did not pass repo: explicitly, the call short-circuits
// with a typed error WITHOUT issuing the wire RPC.
//
// Branch is auto-filled ONLY when the resolved target repo equals the
// cwd's repo. The cwd's git HEAD is meaningful for the target only when
// the target IS the cwd's repo, so a cross-repo target (or an unresolvable
// cwd) leaves branch unstamped and falls through to the base graph WITHOUT
// error — the caller passes branch: explicitly to read a cross-repo
// overlay. "Same repo" is determined by RepoResolver basename / path-
// component match, not git identity: a checkout whose directory name does
// not match the graph name (a renamed or forked top-level clone) is treated
// as cross-repo and will NOT auto-stamp a branch — pass branch: explicitly
// to read its overlay.
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
//  2. Graph-selector gate (for search / query / traverse): only proceed when
//     args["graph"] is missing OR equals "code". Knowledge / cloud /
//     cicd / practice / linkage variants pass through.
//
//  3. Repo injection: missing/empty args["repo"] → ResolveCwd. Hit
//     populates args["repo"]; miss returns typed error.
//
//  4. Branch injection (same-repo only): missing/empty args["branch"]
//     auto-fills via coderun.DetectBranch ONLY when the resolved target
//     repo equals the cwd's repo (resolveRepo reused from step 3). On that
//     same-repo path, success populates args["branch"] and a non-git cwd
//     returns the branch-required typed error. A cross-repo target — or an
//     unresolvable cwd — leaves branch unstamped and falls through to base
//     WITHOUT error.
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

	// Graph-selector gate: search / query / traverse can target non-code graphs.
	// Only proceed when graph is empty (code-graph default for code-only
	// tools is enforced server-side) or explicitly "code".
	if params.Name == "search" || params.Name == "query" || params.Name == "traverse" {
		graph := decodeStringField(args, "graph")
		if graph != "" && graph != "code" {
			return params, false, kgtools.ToolResult{}
		}
	}

	cwd := effectiveCwd(ctx, deps)

	// Step 3: Repo injection.
	if decodeStringField(args, "repo") == "" {
		repo, ok, err := resolveRepo(ctx, deps, cwd)
		if err != nil {
			return params, true, errorResult(params.Name + ": resolve repo: " + err.Error())
		}
		if !ok {
			return params, true, errorResult(params.Name + ": graph=\"code\" requires repo. For the memory/decision/thought graph, use graph=\"knowledge\". For a code repo named \"knowledge\", use graph=\"code\", repo=\"knowledge\".")
		}
		setStringField(args, "repo", repo)
	}

	// Step 4: Branch injection — confined to the same-repo case (see
	// injectSameRepoBranch).
	if decodeStringField(args, "branch") == "" {
		if handled, res := injectSameRepoBranch(ctx, deps, params.Name, cwd, args); handled {
			return params, true, res
		}
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

// injectSameRepoBranch stamps args["branch"] from the cwd's git HEAD, but
// ONLY when the resolved target repo equals the cwd's repo. The cwd's git
// HEAD is meaningful for the target only when the target IS the cwd's repo;
// resolveRepo reuses the same mutex-memoized ResolveCwd already fired in
// step 3 (this second call is free once the load has succeeded). Three cases:
//   - SAME-REPO, git cwd: DetectBranch succeeds → stamp branch.
//   - SAME-REPO, non-git cwd: DetectBranch errors/empty → return the typed
//     branch-required short-circuit (handled==true).
//   - CROSS-REPO (target != cwdRepo, OR cwd unresolvable so cwdOK==false):
//     no stamp AND no error — fall through (handled==false) so resolveCode
//     Retrieves base. The caller passes branch: explicitly for a cross-repo
//     overlay.
//
// A resolver load failure propagates as the typed "resolve repo" short-circuit.
func injectSameRepoBranch(ctx context.Context, deps ClientDeps, toolName, cwd string, args map[string]json.RawMessage) (bool, kgtools.ToolResult) {
	effectiveTarget := decodeStringField(args, "repo")
	cwdRepo, cwdOK, err := resolveRepo(ctx, deps, cwd)
	if err != nil {
		return true, errorResult(toolName + ": resolve repo: " + err.Error())
	}
	if !cwdOK || cwdRepo != effectiveTarget {
		// Cross-repo (or unresolvable cwd): no stamp, no error.
		return false, kgtools.ToolResult{}
	}
	branch, err := coderun.DetectBranch(ctx, cwd)
	if err != nil {
		return true, errorResult(toolName + ": branch is required; run from inside a git working tree or pass branch: (" + err.Error() + ")")
	}
	if branch == "" {
		return true, errorResult(toolName + ": branch is required; run from inside a git working tree or pass branch:")
	}
	setStringField(args, "branch", branch)
	return false, kgtools.ToolResult{}
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

// populateStaleness fills current_head / uncommitted_count from coderun
// helpers when the staleness opt-in is set. Each helper degrades gracefully
// (returns "" / 0 on non-git cwd) so missing git state never blocks a search.
//
// commits_behind is deliberately NOT populated here: the real commits-behind
// signal now rides the code-search staleness footer (codeStalenessFooter),
// which computes it against the recorded sync_commit read off the GraphInfo
// catalog. The prior CommitsBehind(cwd, "") call could only ever report 0
// (empty sync_commit short-circuits), so it was a misleading no-op.
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
// list_branches / delete_branch always need the repo (carried in `name:`).
// For other manage operations (status, pprof_*, log ops,
// set_metadata_overrides, promote_metadata, clear_llm_failures, topology,
// link) this is a no-op fall-through. Branch is never auto-filled —
// delete_branch's branch names the overlay to remove, not the current
// checkout.
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

	if decodeStringField(args, "name") != "" {
		return params, false, kgtools.ToolResult{}
	}

	repo, ok, err := resolveRepo(ctx, deps, effectiveCwd(ctx, deps))
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
