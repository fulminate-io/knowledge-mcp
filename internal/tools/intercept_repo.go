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
// Branch is auto-filled from the MACHINE-LOCAL MANIFEST, not from cwd: when the
// caller omits branch, the repo name is looked up in ~/.knowledge/repos.json
// (populated at collect) for its real on-disk directory, and coderun.DetectBranch
// reads that checkout's current branch. This is machine-correct for any repo —
// including cross-repo targets and renamed/forked clones — because the manifest
// records where the repo actually lives here, rather than guessing from cwd. A
// repo absent from the manifest, or a checkout whose branch cannot be determined
// (a non-git directory, or a detached HEAD — a detection failure here even
// though git exits 0 for it), leaves branch unstamped and falls through to the
// base graph WITHOUT error — the caller passes branch: explicitly to read an
// overlay. repo="all" is never branch-stamped.
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
//  4. Branch injection (manifest-based): missing/empty args["branch"]
//     auto-fills via coderun.DetectBranch run in the repo's recorded on-disk
//     directory (lookupRepoDir → ~/.knowledge/repos.json). A manifest miss, a
//     detection failure (a non-git directory, or a detached HEAD), or
//     repo="all" leaves branch unstamped and falls through to the base graph
//     WITHOUT error. Never inferred from cwd.
//
//  5. Graph materialization (search only): a `search` that got past the gate
//     targets the code graph — an omitted graph was the code default, an
//     explicit one was already "code" — so args["graph"] is set to "code".
//     This is the ONE field written rather than merely filled: it records a
//     routing decision this function MAKES, so that nothing downstream has to
//     re-derive it from the raw field (where omitted reads as knowledge).
//     Non-code searches never reach it; the gate returned them above.
//
//  6. Staleness trio: ONLY when tool == "search" AND args["staleness"]
//     is true. Populates current_head / uncommitted_count /
//     commits_behind from coderun helpers. Skips all three subprocess
//     calls when staleness is unset/false.
//
//  7. Explicit-value preservation: caller-supplied values are never
//     overwritten. Resolver / detect / staleness helpers only fill
//     MISSING fields. The graph write in step 5 is the sole exception, and
//     only ever writes the value the caller's own shape already implied.
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

	// Graph-selector gate. The DEFAULT graph differs per tool, so an OMITTED
	// graph must route differently: `search` defaults to the CODE graph (omitted
	// → code → repo required below), while `query` and `traverse` default to the
	// KNOWLEDGE graph (omitted → knowledge → pass through untouched, no repo).
	// Only an EXPLICIT graph="code" pulls query/traverse onto the repo-required
	// path. (file_symbols is code-only with no graph arg — it always requires
	// repo below.)
	switch params.Name {
	case "search":
		if g := decodeStringField(args, "graph"); g != "" && g != "code" {
			return params, false, kgtools.ToolResult{}
		}
	case "query", "traverse":
		if decodeStringField(args, "graph") != "code" {
			return params, false, kgtools.ToolResult{}
		}
	}

	// Repo is REQUIRED explicitly. A code graph is addressed by NAME; the client
	// never infers it from cwd. The old cwd→graph-name guess was unreliable — it
	// returned empty in the cloud-mode daemon (the catalog enumeration is empty
	// there) and could silently mis-target in general — so an omitted repo now
	// fails loud rather than guessing.
	repo := decodeStringField(args, "repo")
	if repo == "" {
		return params, true, errorResult(params.Name + ": graph=\"code\" requires repo. Pass repo=\"<name>\" (or repo=\"all\" to search every code repo). For the memory/decision/thought graph, use graph=\"knowledge\".")
	}

	// Materialize the search arm's graph decision. Reaching this line on a
	// `search` means the graph-selector gate above already concluded this call
	// targets the code graph — it is why the repo requirement was just enforced.
	// Writing that conclusion onto the args is what stops the next reader in the
	// chain from re-deriving it from the raw field, where an omitted graph reads
	// as the KNOWLEDGE default and the search is answered from the wrong corpus.
	// Only `search` defaults to code; query/traverse arrive here solely with an
	// explicit graph="code", so their args already say so.
	if params.Name == "search" {
		args["graph"] = json.RawMessage(`"code"`)
	}

	// Branch auto-detect (machine-correct, manifest-based). Repo STAYS explicitly
	// required above — this does NOT re-introduce repo inference. When the caller
	// omitted branch, the repo's real on-disk dir from the machine-local manifest
	// drives DetectBranch so the searched repo's ACTUAL branch is stamped; on a
	// manifest miss, a detection failure (a detached HEAD is one), or repo="all"
	// it stays unset (→ base graph).
	if decodeStringField(args, "branch") == "" && repo != "all" {
		if branch := autoDetectBranch(ctx, repo); branch != "" {
			if b, mErr := json.Marshal(branch); mErr == nil {
				args["branch"] = b
			}
		}
	}

	// Staleness trio (search only, staleness:true only): opt-in git state for the
	// staleness footer, read from the session cwd. This is reporting, not repo
	// resolution, so it stays.
	if params.Name == "search" && decodeBoolField(args, "staleness") {
		populateStaleness(ctx, args, effectiveCwd(ctx, deps))
	}

	rewritten, err := json.Marshal(args)
	if err != nil {
		return params, true, errorResult(params.Name + ": re-encode args: " + err.Error())
	}
	params.Arguments = rewritten
	return params, false, kgtools.ToolResult{}
}

// branchDetectState is WHY autoDetectBranchReason answered the way it did. It
// exists because an empty branch alone cannot tell a caller whether this machine
// simply has no checkout of the repo — in which case the base graph is the whole
// answer and nothing is missing — or whether the manifest named a checkout that
// could not be read, in which case an overlay may exist and we failed to find
// out. Those two deserve different treatment, and only one of them is worth
// telling the user about.
type branchDetectState int

const (
	// branchDetected: the manifest named a checkout and git reported its branch.
	branchDetected branchDetectState = iota
	// branchNoManifestEntry: the repo has no manifest entry, so this machine holds
	// no local checkout of it. Not a failure — there is no overlay to miss.
	branchNoManifestEntry
	// branchDetectFailed: the manifest promised a checkout it could not deliver.
	branchDetectFailed
)

// autoDetectBranchReason returns the current git branch of the repo recorded in
// the machine-local manifest (~/.knowledge/repos.json) together with the state
// that explains the answer. Pure read — no cwd inference.
//
// What each state returns:
//   - branchDetected → (branch, branchDetected).
//   - branchNoManifestEntry → ("", branchNoManifestEntry): the repo is not in the
//     manifest at all.
//   - branchDetectFailed → ("", branchDetectFailed): DetectBranch errored (the
//     recorded directory is gone or is not a git repo), OR the checkout is on a
//     detached HEAD — a detection failure HERE even though git exits 0 for it and
//     reports the literal branch name "HEAD". The empty branch is the operative
//     half of that second case: returning "HEAD" would leave every caller
//     stamping "HEAD" as though it were a real branch.
func autoDetectBranchReason(ctx context.Context, repo string) (string, branchDetectState) {
	dir, ok := lookupRepoDir(repo)
	if !ok {
		return "", branchNoManifestEntry
	}
	branch, err := coderun.DetectBranch(ctx, dir)
	if err != nil {
		return "", branchDetectFailed
	}
	if branch == "HEAD" {
		return "", branchDetectFailed
	}
	return branch, branchDetected
}

// autoDetectBranch is the branch-only view of autoDetectBranchReason, for the
// callers that have nothing to do with the reason. One derivation, not two.
func autoDetectBranch(ctx context.Context, repo string) string {
	branch, _ := autoDetectBranchReason(ctx, repo)
	return branch
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

// injectManageRepo handles the manage tool's code-graph subdispatch.
// list_branches / delete_branch address a code graph by NAME (carried in
// `name:`), which is REQUIRED — the client never infers it from cwd. For other
// manage operations this is a no-op fall-through. Branch is never auto-filled.
//
// Returns the same tuple shape as InjectRepoIfCodeGraph: passthrough on continue,
// (params, true, errResult) on the typed missing-name short-circuit.
func injectManageRepo(_ context.Context, _ ClientDeps, params kgtools.CallToolParams) (kgtools.CallToolParams, bool, kgtools.ToolResult) {
	args := decodeRepoArgs(params.Arguments)

	op := decodeStringField(args, "operation")
	if !manageCodeGraphOps[op] {
		return params, false, kgtools.ToolResult{}
	}

	if decodeStringField(args, "name") == "" {
		return params, true, errorResult("manage(" + op + "): repo is required; pass name=\"<repo>\"")
	}
	return params, false, kgtools.ToolResult{}
}
