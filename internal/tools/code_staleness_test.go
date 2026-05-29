// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

	t.Run("recorded → values + ok", func(t *testing.T) {
		exec := graphNamesFake([]*knowledgev1.GraphInfo{
			{Name: "knowledge", SyncCommit: "abc123", SyncTime: ts},
		})
		sc, st, ok := recordedSyncMeta(ctx, exec, "knowledge")
		if !ok || sc != "abc123" || st != ts {
			t.Fatalf("got (%q, %d, %v), want (abc123, %d, true)", sc, st, ok, ts)
		}
	})

	t.Run("unrecorded graph → degrade", func(t *testing.T) {
		exec := graphNamesFake([]*knowledgev1.GraphInfo{{Name: "knowledge"}})
		if _, _, ok := recordedSyncMeta(ctx, exec, "knowledge"); ok {
			t.Fatal("expected ok=false for an unrecorded graph")
		}
	})

	t.Run("repo absent → degrade", func(t *testing.T) {
		exec := graphNamesFake([]*knowledgev1.GraphInfo{{Name: "other", SyncCommit: "x"}})
		if _, _, ok := recordedSyncMeta(ctx, exec, "knowledge"); ok {
			t.Fatal("expected ok=false when repo not in catalog")
		}
	})

	t.Run("nil exec → degrade", func(t *testing.T) {
		if _, _, ok := recordedSyncMeta(ctx, nil, "knowledge"); ok {
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
	t.Run("recorded sync_commit at HEAD → up to date footer with last-collected", func(t *testing.T) {
		exec := graphNamesFake([]*knowledgev1.GraphInfo{
			{Name: "knowledge", SyncCommit: head, SyncTime: time.Now().Add(-2 * time.Hour).UnixNano()},
		})
		footer := codeStalenessFooter(ctx, exec, root, "knowledge")
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
			{Name: "knowledge"}, // empty SyncCommit + zero SyncTime
		})
		if footer := codeStalenessFooter(ctx, exec, root, "knowledge"); footer != "" {
			t.Fatalf("expected empty footer for unrecorded carrier, got %q", footer)
		}
	})

	t.Run("repo absent from catalog → no footer", func(t *testing.T) {
		exec := graphNamesFake([]*knowledgev1.GraphInfo{
			{Name: "other", SyncCommit: head},
		})
		if footer := codeStalenessFooter(ctx, exec, root, "knowledge"); footer != "" {
			t.Fatalf("expected empty footer when repo not in catalog, got %q", footer)
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
