// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/coderun"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/session"
)

// effectiveCwd is the cwd repo resolution uses: the per-session workspace cwd
// carried on ctx (HTTP transport, set by the daemon from peer-cwd resolution)
// when present, else the process-global --root (deps.RootDir(), the
// no-session-cwd default). This is what lets two concurrent HTTP sessions from
// different repos each resolve their own code graph: a call carrying no
// workspace cwd falls back to --root exactly as before.
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
// process --root (deps.RootDir(), the no-session-cwd default) — see effectiveCwd.
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
// A branch the CALLER supplied is VALIDATED rather than auto-filled, and it is
// the one branch input that can refuse a call: it must name either a local ref
// of that repo or a collected branch graph. Naming neither is refused here
// instead of being answered from the base graph under a header claiming the
// branch — see resolveBranchArg and validateCallerSuppliedBranch.
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
//  4. Branch injection or validation (resolveBranchArg). MISSING/empty
//     args["branch"] auto-fills via coderun.DetectBranch run in the repo's
//     recorded on-disk directory (lookupRepoDir → ~/.knowledge/repos.json). A
//     manifest miss, a detection failure (a non-git directory, or a detached
//     HEAD), or repo="all" leaves branch unstamped and falls through to the base
//     graph WITHOUT error. Never inferred from cwd. A SUPPLIED branch is instead
//     validated against the repo's local refs and its collected branch graphs,
//     and a branch in neither returns a typed error. A call with NO branch-scoped
//     read is skipped outright — see hasNoBranchScopedRead, which excludes
//     query(mode:"topology") because its arm refuses branch rather than drop it.
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

	// Branch: auto-fill a missing one, validate a supplied one. See resolveBranchArg.
	if err := resolveBranchArg(ctx, deps, params.Name, repo, args); err != nil {
		return params, true, errorResult(params.Name + ": " + err.Error())
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

// resolveBranchArg settles the branch for a code-graph call: it auto-fills a
// MISSING branch and VALIDATES a caller-supplied one. It returns an error only
// for the second case, and only when the supplied branch names nothing readable.
//
// THE TWO BRANCHES ARE NOT SYMMETRIC, and that asymmetry is the point. An
// auto-detected branch was read out of the repo's own checkout by
// coderun.DetectBranch, so it is a ref by construction and re-checking it would
// spend an exec and an RPC to re-derive a fact this package just produced. A
// caller-supplied branch has been through no such check: server-side an unknown
// branch falls back to the base graph and is rendered under a header naming the
// branch that was asked for, so the caller gets a plausible payload about a
// branch that does not exist.
//
// Auto-fill stays silent on failure — a manifest miss, a non-git directory, or a
// detached HEAD leaves branch unset and the read falls through to the base
// graph. That is unchanged; only the supplied-branch arm can refuse.
//
// EXTRACTED RATHER THAN INLINE because the combined form nests past the limit
// the linter enforces, and because this file already keeps its branch logic in
// helpers (autoDetectBranchReason, autoDetectBranch, hasNoBranchScopedRead).
func resolveBranchArg(ctx context.Context, deps ClientDeps, tool, repo string, args map[string]json.RawMessage) error {
	if repo == "all" || hasNoBranchScopedRead(tool, args) {
		return nil
	}
	branch := decodeStringField(args, "branch")
	if branch == "" {
		if detected := autoDetectBranch(ctx, repo); detected != "" {
			if b, mErr := json.Marshal(detected); mErr == nil {
				args["branch"] = b
			}
		}
		return nil
	}
	return validateCallerSuppliedBranch(ctx, deps, repo, branch)
}

// validateCallerSuppliedBranch reports whether branch names something a read of
// repo can actually be scoped to, and returns a refusal or a probe error when it
// does not.
//
// THE VOCABULARY IS SPLIT ACROSS THE TWO SIDES, WHICH IS WHY THE CHECK IS HERE
// AND NOT AT THE SERVER'S RESOLVER. The set of real branches lives in git, which
// only this side can reach — it holds the machine-local repo manifest. The set of
// collected branch graphs lives on the server. A branch can legitimately be in
// either one alone: a locally-deleted branch whose branch graph survives is still
// readable, and a real local branch that was never collected is not. So the
// question is union membership, and the client is the only side that can consult
// both. The server keeps falling back to base for a branch it has no graph for,
// exactly as its own regression guard pins.
//
// THREE OUTCOMES, AND THE THIRD IS NOT THE SECOND. Accept; refuse as bad input
// once BOTH vocabularies were successfully consulted and neither had it; or ERROR
// as unverifiable when a PROBE FAILED, naming what could not be read. A probe
// failure rendered as "is not a branch" would be a false explanation of a state
// nobody observed, and accepting on a failed probe would be a silent fallback.
//
// A MANIFEST MISS IS NOT A PROBE FAILURE. There is simply no checkout on this
// machine to consult, so the git vocabulary is unavailable by absence and the
// branch-graph set decides alone. The refusal says which vocabularies were
// consulted so the caller can tell the two situations apart.
func validateCallerSuppliedBranch(ctx context.Context, deps ClientDeps, repo, branch string) error {
	gitConsulted := false
	if dir, ok := lookupRepoDir(repo); ok {
		exists, err := coderun.BranchExists(ctx, dir, branch)
		if err != nil {
			return fmt.Errorf("branch %q cannot be verified for repo %q: the recorded checkout %q could not be read as a git repository (%v). Refusing rather than reporting a membership this client could not check", branch, repo, dir, err)
		}
		if exists {
			return nil
		}
		gitConsulted = true
	}

	ix, err := manageIndexer(deps)
	if err != nil {
		return fmt.Errorf("branch %q cannot be verified for repo %q: the branch-graph list could not be read (%v). Refusing rather than reporting a membership this client could not check", branch, repo, err)
	}
	resp, ierr := ix.Index(ctx, &knowledgev1.IndexRequest{
		Target:    branchGraphSelector(manageArgs{Name: repo}),
		Operation: knowledgev1.IndexRequest_INDEX_OP_LIST_BRANCHES,
	})
	if ierr != nil {
		return fmt.Errorf("branch %q cannot be verified for repo %q: the branch-graph list could not be read (%v). Refusing rather than reporting a membership this client could not check", branch, repo, ierr)
	}

	overlays := resp.GetBranches()
	names := make([]string, 0, len(overlays))
	for _, o := range overlays {
		// GraphInfo.Name carries the BARE branch name — the registry trims the
		// "<repo>@" prefix before setting it — so this is a direct comparison.
		if o.GetName() == branch {
			return nil
		}
		names = append(names, o.GetName())
	}

	consulted := "the branch graphs collected for it"
	if gitConsulted {
		consulted = "its local git refs and the branch graphs collected for it"
	}
	available := "none"
	if len(names) > 0 {
		available = strings.Join(names, ", ")
	}
	return fmt.Errorf("branch %q is not a branch of repo %q and no branch graph of that name exists"+" (consulted %s; branch graphs available: %s)", branch, repo, consulted, available)
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

// hasNoBranchScopedRead reports whether this call has no branch-scoped read for
// a detected branch to scope, so stamping one would be meaningless.
//
// query(mode:"topology") is the one such shape today. Every analyzer reads
// through foundation.Request, which carries no Branch field, so armTopology
// REFUSES branch rather than accept a scope control it would silently drop.
// Auto-filling it here would turn that refusal on callers who never asked for a
// branch — measured live, it failed EVERY code-graph topology call on a machine
// whose repo manifest resolves. An EXPLICIT branch on a topology call is still
// refused by the arm, which is the intended behavior: this only declines to
// invent one.
func hasNoBranchScopedRead(tool string, args map[string]json.RawMessage) bool {
	return tool == "query" && decodeStringField(args, "mode") == "topology"
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
