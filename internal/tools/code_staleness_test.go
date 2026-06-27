// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/coderun"
)

// graphNamesFake returns a fixed GraphInfo catalog for the modules read that
// recordedSyncMeta issues, so the footer assembly is testable without a server.
func graphNamesFake(infos []*knowledgev1.GraphInfo) func(context.Context, *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	return func(_ context.Context, _ *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
		return &knowledgev1.ExecuteResponse{GraphNames: infos}, nil
	}
}

// repoRootForTest walks up from the test cwd to the git repo root (the dir with
// a .git entry), so coderun.CommitsBehind runs against a real working tree.
func repoRootForTest(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, statErr := os.Stat(filepath.Join(dir, ".git")); statErr == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Skip("not inside a git repo; skipping footer integration test")
		}
		dir = parent
	}
}

// TestRecordedSyncMeta covers the carrier read in isolation: recorded values
// round-trip; an empty carrier, an absent repo, and a not-recorded graph all
// degrade to ok=false (unknown).
func TestRecordedSyncMeta(t *testing.T) {
	ctx := context.Background()
	const ts int64 = 1779999083502863000

	t.Run("recorded collect-meta → values + ok", func(t *testing.T) {
		exec := graphNamesFake([]*knowledgev1.GraphInfo{
			{Name: "knowledge", CollectedCommit: "abc123", CollectedTime: ts},
		})
		sc, st, ok := recordedSyncMeta(ctx, exec, "knowledge", "")
		if !ok || sc != "abc123" || st != ts {
			t.Fatalf("got (%q, %d, %v), want (abc123, %d, true)", sc, st, ok, ts)
		}
	})

	t.Run("only sync-meta (no collect-meta) → degrade", func(t *testing.T) {
		// Proves the repoint actually reads the COLLECT channel: a carrier with
		// only the old SyncCommit/SyncTime set (the cloud sync-receive fields)
		// must now degrade to ok=false, NOT report a stale collect age.
		exec := graphNamesFake([]*knowledgev1.GraphInfo{
			{Name: "knowledge", SyncCommit: "abc123", SyncTime: ts},
		})
		if _, _, ok := recordedSyncMeta(ctx, exec, "knowledge", ""); ok {
			t.Fatal("expected ok=false when only SyncCommit/SyncTime are set (collect-meta empty)")
		}
	})

	t.Run("unrecorded graph → degrade", func(t *testing.T) {
		exec := graphNamesFake([]*knowledgev1.GraphInfo{{Name: "knowledge"}})
		if _, _, ok := recordedSyncMeta(ctx, exec, "knowledge", ""); ok {
			t.Fatal("expected ok=false for an unrecorded graph")
		}
	})

	t.Run("repo absent → degrade", func(t *testing.T) {
		exec := graphNamesFake([]*knowledgev1.GraphInfo{{Name: "other", CollectedCommit: "x"}})
		if _, _, ok := recordedSyncMeta(ctx, exec, "knowledge", ""); ok {
			t.Fatal("expected ok=false when repo not in catalog")
		}
	})

	t.Run("nil exec → degrade", func(t *testing.T) {
		if _, _, ok := recordedSyncMeta(ctx, nil, "knowledge", ""); ok {
			t.Fatal("expected ok=false for nil exec")
		}
	})
}

func TestCodeStalenessFooter(t *testing.T) {
	root := repoRootForTest(t)
	ctx := context.Background()
	head, err := coderun.HeadCommit(ctx, root)
	if err != nil || head == "" {
		t.Skip("no git HEAD available; skipping")
	}
	// The searched repo's git dir is resolved from the manifest first; record the
	// real repo root under its own name so commits-behind runs against THIS tree.
	// This also pins the manifest-dir path (the cross-repo exit-128 fix): git runs
	// in the recorded dir, not the (here-unrelated) cwd.
	repoName := filepath.Base(root)
	m := withTestManifest(t)
	require.NoError(t, m.Record(repoName, root))

	t.Run("recorded collect_commit at HEAD → up to date footer with last-collected", func(t *testing.T) {
		exec := graphNamesFake([]*knowledgev1.GraphInfo{
			{Name: repoName, CollectedCommit: head, CollectedTime: time.Now().Add(-2 * time.Hour).UnixNano()},
		})
		// cwd is an UNRELATED dir to prove git runs in the manifest-recorded dir.
		footer := codeStalenessFooter(ctx, exec, t.TempDir(), repoName, "")
		if footer == "" {
			t.Fatal("expected a footer when sync_commit is recorded")
		}
		if !strings.Contains(footer, "code index:") {
			t.Fatalf("footer missing prefix: %q", footer)
		}
		if !strings.Contains(footer, "2 hours ago") {
			t.Fatalf("footer missing last-collected age: %q", footer)
		}
		// HEAD..HEAD is 0 commits behind → up to date.
		if !strings.Contains(footer, "up to date") {
			t.Fatalf("footer should report up to date for HEAD sync_commit: %q", footer)
		}
	})

	t.Run("no recorded metadata → no footer (degrade)", func(t *testing.T) {
		exec := graphNamesFake([]*knowledgev1.GraphInfo{
			{Name: repoName}, // empty SyncCommit + zero SyncTime
		})
		if footer := codeStalenessFooter(ctx, exec, root, repoName, ""); footer != "" {
			t.Fatalf("expected empty footer for unrecorded carrier, got %q", footer)
		}
	})

	t.Run("repo absent from catalog → no footer", func(t *testing.T) {
		exec := graphNamesFake([]*knowledgev1.GraphInfo{
			{Name: "other", CollectedCommit: head},
		})
		if footer := codeStalenessFooter(ctx, exec, root, repoName, ""); footer != "" {
			t.Fatalf("expected empty footer when repo not in catalog, got %q", footer)
		}
	})

	t.Run("dir unknown (not in manifest, not cwd's repo) → collection time WITHOUT commits-behind", func(t *testing.T) {
		// LOAD-BEARING (the cross-repo exit-128 fix): a recorded sync_commit but
		// NO resolvable git dir must report the collection time alone — NEVER run
		// git in the wrong tree and surface "commits-behind unavailable: exit
		// status 128". "elsewhere" is not in the (test) manifest and is not the
		// cwd's repo.
		withTestManifest(t) // fresh empty manifest for this subtest
		exec := graphNamesFake([]*knowledgev1.GraphInfo{
			{Name: "elsewhere", CollectedCommit: head, CollectedTime: time.Now().Add(-3 * time.Hour).UnixNano()},
		})
		footer := codeStalenessFooter(ctx, exec, root, "elsewhere", "")
		if !strings.Contains(footer, "last collected 3 hours ago") {
			t.Fatalf("expected a last-collected footer, got %q", footer)
		}
		if strings.Contains(footer, "exit status 128") || strings.Contains(footer, "commits-behind unavailable") {
			t.Fatalf("must NOT run git in an unknown dir (no exit-128), got %q", footer)
		}
		if strings.Contains(footer, "up to date") || strings.Contains(footer, "behind HEAD") {
			t.Fatalf("commits-behind must be skipped when the dir is unknown, got %q", footer)
		}
	})

	t.Run("branch read filters the repo@branch overlay entry", func(t *testing.T) {
		// With a branch passed, recordedSyncMeta filters for the "repo@branch"
		// overlay entry (the overlay_of enumeration). The base entry must NOT
		// satisfy a branch read — proving the branch is threaded through.
		exec := graphNamesFake([]*knowledgev1.GraphInfo{
			{Name: repoName, CollectedCommit: head, CollectedTime: time.Now().UnixNano()},                                  // base, wrong for a branch read
			{Name: repoName + "@feature", CollectedCommit: head, CollectedTime: time.Now().Add(-1 * time.Hour).UnixNano()}, // the overlay entry
		})
		footer := codeStalenessFooter(ctx, exec, t.TempDir(), repoName, "feature")
		if !strings.Contains(footer, "1 hour ago") {
			t.Fatalf("branch read must surface the repo@branch overlay entry's collect time, got %q", footer)
		}
	})

	t.Run("branch read with only a base entry degrades (no overlay match)", func(t *testing.T) {
		// A branch read where only the BASE entry exists must degrade to no footer
		// — it must NOT fall back to the base entry's meta (that's the stale-base
		// lie this fixes).
		exec := graphNamesFake([]*knowledgev1.GraphInfo{
			{Name: repoName, CollectedCommit: head, CollectedTime: time.Now().UnixNano()},
		})
		if footer := codeStalenessFooter(ctx, exec, t.TempDir(), repoName, "feature"); footer != "" {
			t.Fatalf("a branch read must not fall back to the base entry, got %q", footer)
		}
	})
}

func TestRelativeAgeSince(t *testing.T) {
	const day = 24 * time.Hour
	cases := []struct {
		name string
		d    time.Duration
		want string
	}{
		{"sub-minute", 30 * time.Second, "just now"},
		{"one minute", time.Minute, "1 minute ago"},
		{"several minutes", 5 * time.Minute, "5 minutes ago"},
		{"one hour", time.Hour, "1 hour ago"},
		{"several hours", 3 * time.Hour, "3 hours ago"},
		{"one day", day, "1 day ago"},
		{"several days", 2 * day, "2 days ago"},
		{"one week", 7 * day, "1 week ago"},
		{"several weeks", 21 * day, "3 weeks ago"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := relativeAgeSince(tc.d); got != tc.want {
				t.Fatalf("relativeAgeSince(%v) = %q, want %q", tc.d, got, tc.want)
			}
		})
	}
}

func TestRelativeAge(t *testing.T) {
	// A recent instant resolves to "just now"; an old one to a non-empty
	// bucketed string. Zero time degrades to "just now" (never negative).
	if got := relativeAge(time.Now()); got != "just now" {
		t.Fatalf("relativeAge(now) = %q, want %q", got, "just now")
	}
	if got := relativeAge(time.Now().Add(-3 * time.Hour)); got != "3 hours ago" {
		t.Fatalf("relativeAge(-3h) = %q, want %q", got, "3 hours ago")
	}
	if got := relativeAge(time.Time{}); got != "just now" {
		t.Fatalf("relativeAge(zero) = %q, want %q", got, "just now")
	}
}
