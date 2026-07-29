// SPDX-License-Identifier: Apache-2.0

// Package tools — code_staleness.go reads a code graph's recorded collection
// metadata (HEAD SHA + collection time) back off the GraphInfo catalog and
// turns it into a human-readable staleness signal. The collect path records
// SyncCommitKey / SyncTimeKey onto code-graph metadata; the server surfaces
// these as the COLLECT-meta channel (GraphInfo.CollectedCommit /
// CollectedTime) on the carrier returned by the modules catalog read —
// distinct from GraphInfo.SyncTime, which carries sync-receive time
// (sync_list's "Last synced"). This helper is the single
// client-side reader shared by both consumers: the code-search staleness
// footer and the `knowledge doctor` staleness check.

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/coderun"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
)

// recordedSyncMeta reads the recorded collected_commit + collected_time for a
// single code graph via the generic modules catalog read (query{graph:code, mode:modules}
// → RETURN_MODE_GRAPH_NAMES → DecodeGraphNames). ok is false (degrade-to-unknown)
// on any miss: no seam, decode failure, repo not found, or an empty/zero recorded
// pair (a graph collected before staleness tracking landed).
//
// Branch-aware: when branch is non-empty, the read passes overlay_of=repo so the
// catalog enumerates the repo's "<repo>@*" overlay keys, and we filter for the
// "<repo>@<branch>" overlay entry — so the footer reports the searched BRANCH
// overlay's collect meta, not the stale base. An empty branch keeps the base
// enumeration (filter on name == repo).
//
// NOTE: the overlay path depends on the server populating overlay entries' collect
// meta (listOverlays → readSyncMeta). Until that server-side enablement lands,
// overlay entries surface with empty collect meta and this degrades to ok=false
// for a branch read — the client-side wire is in place and becomes live the moment
// the server fills overlay collect meta.
func recordedSyncMeta(ctx context.Context, exec engine.ExecuteFn, repo, branch string) (syncCommit string, syncTime int64, ok bool) {
	if exec == nil || repo == "" {
		return "", 0, false
	}
	modulesArgs := map[string]any{"graph": "code", "mode": "modules"}
	// Branch overlays are keyed "<repo>@<branch>"; overlay_of restricts the
	// catalog enumeration to the repo's overlay keys so the branch entry is in
	// the returned set. wantName is the entry we filter for.
	wantName := repo
	if branch != "" {
		modulesArgs["overlay_of"] = repo
		wantName = repo + "@" + branch
	}
	args, err := json.Marshal(modulesArgs)
	if err != nil {
		return "", 0, false
	}
	req, compiled := engine.Compile("query", args)
	if !compiled {
		return "", 0, false
	}
	resp, err := exec(ctx, req)
	if err != nil {
		return "", 0, false
	}
	infos, err := engine.DecodeGraphNames(resp)
	if err != nil {
		return "", 0, false
	}
	for _, gi := range infos {
		if gi.GetName() != wantName {
			continue
		}
		sc, st := gi.GetCollectedCommit(), gi.GetCollectedTime()
		if sc == "" && st == 0 {
			return "", 0, false
		}
		return sc, st, true
	}
	return "", 0, false
}

// codeStalenessFooter renders a one-line "code index" staleness footer for the
// searched repo (+ branch overlay), or "" to degrade silently (no recorded
// metadata → no footer, matching the prior graceful-empty behavior). When a
// sync_commit is recorded it computes real commits-behind by running git in the
// searched repo's REAL directory; when that count can't be computed (detached
// HEAD, shallow clone, unknown revision) it falls back to the last-collected-when
// signal alone with the reason noted.
//
// Directory resolution (the cross-repo git-exit-128 fix): git MUST run in the
// SEARCHED repo's dir, not the session cwd — running rev-list in cwd for a
// cross-repo search produced "exit status 128". The dir is resolved from the
// machine-local manifest (lookupRepoDir(repo)); the session cwd is used ONLY when
// it IS the searched repo's checkout (its basename matches repo). When neither
// source yields the repo's dir, CommitsBehind is SKIPPED entirely (report the
// collection time alone) rather than running git in the wrong tree and surfacing
// a misleading exit-128.
func codeStalenessFooter(ctx context.Context, exec engine.ExecuteFn, cwd, repo, branch string) string {
	syncCommit, syncTime, ok := recordedSyncMeta(ctx, exec, repo, branch)
	if !ok {
		return ""
	}
	when := "unknown"
	if syncTime != 0 {
		when = relativeAge(time.Unix(0, syncTime))
	}
	if syncCommit == "" {
		return fmt.Sprintf("code index: last collected %s", when)
	}
	gitDir, dirKnown := stalenessGitDir(cwd, repo)
	if !dirKnown {
		// We can't run git in the searched repo's tree (not in the manifest and
		// not the cwd's repo). Report the collection time WITHOUT a bogus
		// commits-behind — no exit-128, no misleading count.
		return fmt.Sprintf("code index: last collected %s", when)
	}
	behind, err := coderun.CommitsBehind(ctx, gitDir, syncCommit)
	if err != nil {
		return fmt.Sprintf("code index: last collected %s (commits-behind unavailable: %v)", when, err)
	}
	if behind == 0 {
		// COMMITS ARE NOT THE COLLECTION UNIT. Collection indexes the
		// WORKING TREE — DiscoverFiles enumerates via `git ls-files --cached --others
		// --exclude-standard`, which includes untracked files — while this footer
		// measures freshness in COMMITS. The two disagree about what "the tree" is, so
		// uncommitted and untracked edits never move `behind` and the footer reported
		// "up to date" beside searches that could not see 289-line files sitting on
		// disk. A freshness signal that cannot go stale removes the operator's only cue
		// to re-collect, which is worse than one that admits it does not know.
		//
		// The honest downgrade is UNCERTAINTY, not staleness: a dirty tree does not
		// prove the index is behind, because the collect may well have run after those
		// edits. UncommittedCount only counts TRACKED modifications (`git diff
		// --name-only`), so it under-reports relative to what collection actually
		// walks — it is a floor, and a non-zero floor is enough to withdraw the claim.
		// A read error is not itself evidence of anything, so it falls through.
		if dirty, derr := coderun.UncommittedCount(ctx, gitDir); derr == nil && dirty > 0 {
			return fmt.Sprintf(
				"code index: last collected %s at this commit; %d uncommitted file(s) — freshness not confirmable from the commit count alone",
				when, dirty)
		}
		return fmt.Sprintf("code index: up to date — last collected %s", when)
	}
	return fmt.Sprintf("code index: %s behind HEAD — last collected %s", pluralCommits(behind), when)
}

// stalenessGitDir resolves the directory git should run in for the searched
// repo's commits-behind count, and reports whether it is known. Preference:
//  1. the machine-local manifest entry for repo (where it was collected from on
//     THIS machine) — the authoritative cross-repo source.
//  2. the session cwd, ONLY when cwd IS the searched repo's checkout (its
//     basename equals repo, or repo appears as a path component) — covers the
//     same-repo search before a collect has populated the manifest.
//
// Returns ok=false when neither applies, so the caller skips CommitsBehind rather
// than running git in an unrelated tree (the cross-repo exit-128 bug).
func stalenessGitDir(cwd, repo string) (dir string, ok bool) {
	if repo == "" || repo == "all" {
		return "", false
	}
	if d, found := lookupRepoDir(repo); found {
		return d, true
	}
	if cwd != "" && (filepath.Base(cwd) == repo ||
		strings.HasSuffix(cwd, "/"+repo) ||
		strings.Contains(cwd, "/"+repo+"/")) {
		return cwd, true
	}
	return "", false
}

// pluralCommits formats "<n> commit[s]".
func pluralCommits(n int) string {
	if n == 1 {
		return "1 commit"
	}
	return fmt.Sprintf("%d commits", n)
}

// RecordedCodeSyncMeta is the exported seam over recordedSyncMeta for callers
// outside package tools (the knowledge doctor staleness check). It returns the
// recorded HEAD SHA and collection time for a code graph's BASE (branch="" → no
// overlay), or ok=false to degrade to unknown. The doctor check is base-graph
// scoped; the branch-overlay read is internal to the search footer.
func RecordedCodeSyncMeta(ctx context.Context, exec engine.ExecuteFn, repo string) (syncCommit string, collectedAt time.Time, ok bool) {
	sc, st, found := recordedSyncMeta(ctx, exec, repo, "")
	if !found {
		return "", time.Time{}, false
	}
	var t time.Time
	if st != 0 {
		t = time.Unix(0, st)
	}
	return sc, t, true
}

// RelativeAge is the exported seam over relativeAge for callers outside package
// tools (the knowledge doctor staleness check).
func RelativeAge(t time.Time) string { return relativeAge(t) }

// relativeAge renders a wall-clock instant as a coarse "<n> <unit> ago" string
// for the "last collected <when>" staleness signal. Sub-minute resolves to
// "just now"; minutes, hours, days, and weeks each get their own bucket. A zero
// or future time returns "just now" so callers never surface a negative age.
func relativeAge(t time.Time) string {
	if t.IsZero() {
		return "just now"
	}
	return relativeAgeSince(time.Since(t))
}

// relativeAgeSince is the duration-based core of relativeAge, split out so the
// thresholds are unit-testable without constructing wall-clock instants.
func relativeAgeSince(d time.Duration) string {
	const day = 24 * time.Hour
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return pluralAge(int(d/time.Minute), "minute")
	case d < day:
		return pluralAge(int(d/time.Hour), "hour")
	case d < 7*day:
		return pluralAge(int(d/day), "day")
	default:
		return pluralAge(int(d/(7*day)), "week")
	}
}

// pluralAge formats "<n> <unit>[s] ago".
func pluralAge(n int, unit string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s ago", unit)
	}
	return fmt.Sprintf("%d %ss ago", n, unit)
}
