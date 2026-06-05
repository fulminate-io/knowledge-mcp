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
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/coderun"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
)

// recordedSyncMeta reads the recorded collected_commit + collected_time for a
// single code graph via the generic modules catalog read (query{graph:code, mode:modules}
// → RETURN_MODE_GRAPH_NAMES → DecodeGraphNames). It filters the returned
// []GraphInfo by graph name == repo and returns the recorded values. ok is
// false (degrade-to-unknown) on any miss: no seam, decode failure, repo not
// found, or an empty/zero recorded pair (a graph collected before staleness
// tracking landed).
func recordedSyncMeta(ctx context.Context, exec engine.ExecuteFn, repo string) (syncCommit string, syncTime int64, ok bool) {
	if exec == nil || repo == "" {
		return "", 0, false
	}
	args, err := json.Marshal(map[string]any{"graph": "code", "mode": "modules"})
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
		if gi.GetName() != repo {
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
// searched repo, or "" to degrade silently (no recorded metadata → no footer,
// matching the prior graceful-empty behavior). When a sync_commit is recorded
// it computes real commits-behind against the cwd's HEAD; when that count can't
// be computed (detached HEAD, shallow clone, unknown revision) it falls back to
// the last-collected-when signal alone with the reason noted.
func codeStalenessFooter(ctx context.Context, exec engine.ExecuteFn, cwd, repo string) string {
	syncCommit, syncTime, ok := recordedSyncMeta(ctx, exec, repo)
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
	behind, err := coderun.CommitsBehind(ctx, cwd, syncCommit)
	if err != nil {
		return fmt.Sprintf("code index: last collected %s (commits-behind unavailable: %v)", when, err)
	}
	if behind == 0 {
		return fmt.Sprintf("code index: up to date — last collected %s", when)
	}
	return fmt.Sprintf("code index: %s behind HEAD — last collected %s", pluralCommits(behind), when)
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
// recorded HEAD SHA and collection time for a code graph, or ok=false to
// degrade to unknown.
func RecordedCodeSyncMeta(ctx context.Context, exec engine.ExecuteFn, repo string) (syncCommit string, collectedAt time.Time, ok bool) {
	sc, st, found := recordedSyncMeta(ctx, exec, repo)
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
