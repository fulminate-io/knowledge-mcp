// SPDX-License-Identifier: Apache-2.0

// ast_walkroot_test.go — walk-root resolution tests for the client-side ast
// intercept: resolveRepoDir's absolute/current-tree/manifest/fail-loud chain,
// the defaulted-vs-explicit --root guard, and the walked_root echo + zero-scan
// hint on count and match. Split out of ast_test.go to keep each file under the
// length cap; both files are package tools and share astTestDeps/astFixtureRepo/
// callAst.

package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/session"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestResolveRepoDir covers the cross-repo walk-root resolution: no-repo-arg and
// the current repo's OWN name walk the session cwd; a bare cross-repo NAME is
// NEVER guessed to a directory — it fails loud and points to an absolute path,
// because a repo name is not a portable filesystem path (the same name lives at
// a different location on every machine, and the graph stores no collect-time
// path to map it back); an absolute path walks that dir directly; an empty root
// preserves the existing --root-empty error. Resolution is purely FILESYSTEM-
// based (directory existence + a path match against the real base tree), NOT the
// code-graph catalog — so the fixtures create real temp dirs.
func TestResolveRepoDir(t *testing.T) {
	ctx := context.Background()

	t.Run("no_repo_walks_cwd", func(t *testing.T) {
		// An EXPLICITLY-set root (rootDirSet: true) with no repo arg walks that
		// root. The fail-loud guard only fires when the root is DEFAULTED and no
		// session cwd is known — the defaulted case moves to
		// defaulted_root_omitted_repo_fails_loud below.
		parent := t.TempDir()
		dirY := filepath.Join(parent, "knowledge")
		require.NoError(t, os.MkdirAll(dirY, 0o750))
		deps := astTestDeps{rootDir: dirY, rootDirSet: true}
		got, err := resolveRepoDir(ctx, deps, "")
		require.NoError(t, err)
		assert.Equal(t, dirY, got)
	})

	t.Run("same_repo_walks_cwd", func(t *testing.T) {
		parent := t.TempDir()
		dirY := filepath.Join(parent, "knowledge")
		require.NoError(t, os.MkdirAll(dirY, 0o750))
		deps := astTestDeps{rootDir: dirY}
		got, err := resolveRepoDir(ctx, deps, "knowledge")
		require.NoError(t, err)
		assert.Equal(t, dirY, got)
	})

	t.Run("relative_root_is_anchored_to_abs", func(t *testing.T) {
		// An EXPLICITLY-set relative root "." (rootDirSet: true) is anchored to an
		// absolute path: effectiveCwd can hand back a relative root, and the
		// current-tree path match needs an absolute base (filepath.Dir(".") is "."
		// with no parent), so resolveRepoDir anchors it via filepath.Abs against
		// the process cwd. Chdir into a repo dir, root="." → resolves to that
		// absolute dir, and the repo's own name resolves the same anchored tree.
		// Without the anchor, "knowledge" here fails loud (the live daemon symptom
		// this pins). The DEFAULTED-"." fail-loud scenario is covered separately by
		// defaulted_root_omitted_repo_fails_loud.
		parent := t.TempDir()
		dirY := filepath.Join(parent, "knowledge")
		require.NoError(t, os.MkdirAll(dirY, 0o750))
		t.Chdir(dirY) // process cwd = .../knowledge
		deps := astTestDeps{rootDir: ".", rootDirSet: true}
		// t.TempDir() lives under a /var → /private/var symlink on macOS, and
		// os.Getwd resolves it, so compare via EvalSymlinks.
		wantY, err := filepath.EvalSymlinks(dirY)
		require.NoError(t, err)

		got, err := resolveRepoDir(ctx, deps, "")
		require.NoError(t, err)
		assert.True(t, filepath.IsAbs(got), "a relative root must be anchored to an absolute path, got %q", got)
		gotEval, err := filepath.EvalSymlinks(got)
		require.NoError(t, err)
		assert.Equal(t, wantY, gotEval)

		got2, err := resolveRepoDir(ctx, deps, "knowledge")
		require.NoError(t, err)
		got2Eval, err := filepath.EvalSymlinks(got2)
		require.NoError(t, err)
		assert.Equal(t, wantY, got2Eval, "the current repo's name must resolve the anchored current tree")
	})

	t.Run("cross_repo_name_does_not_guess_sibling", func(t *testing.T) {
		// A bare cross-repo NAME must NOT be resolved by guessing a sibling
		// directory: repo names are not portable filesystem paths. Even with a
		// real parent/agent sibling on disk, resolution fails loud and directs the
		// user to an absolute path — a name→dir guess is correct only by accident
		// of THIS machine's layout, so we never make it. The manifest is empty
		// here, so the sibling on disk is NOT picked up.
		withTestManifest(t) // empty manifest: no recorded "agent" entry
		parent := t.TempDir()
		dirY := filepath.Join(parent, "knowledge")
		dirX := filepath.Join(parent, "agent")
		require.NoError(t, os.MkdirAll(dirY, 0o750))
		require.NoError(t, os.MkdirAll(dirX, 0o750))
		deps := astTestDeps{rootDir: dirY}
		got, err := resolveRepoDir(ctx, deps, "agent")
		require.Error(t, err)
		assert.Empty(t, got)
		assert.NotEqual(t, dirX, got, "a cross-repo NAME must never resolve to a guessed sibling dir")
		assert.Contains(t, err.Error(), "absolute checkout path", "the error must point the user to an absolute path")
		assert.Contains(t, err.Error(), "register_repo", "the floor error must point the user to manage(register_repo)")
	})

	t.Run("cross_repo_no_checkout_errors", func(t *testing.T) {
		// LOAD-BEARING: parent/agent does NOT exist on disk. The fail-loud floor
		// MUST error rather than silently returning the cwd (knowledge) tree.
		// This pins the never-return-the-cwd-tree property — it fails if the
		// floor regresses.
		withTestManifest(t) // empty manifest
		parent := t.TempDir()
		dirY := filepath.Join(parent, "knowledge")
		require.NoError(t, os.MkdirAll(dirY, 0o750))
		// Deliberately do NOT create parent/agent.
		deps := astTestDeps{rootDir: dirY}
		got, err := resolveRepoDir(ctx, deps, "agent")
		require.Error(t, err)
		assert.Empty(t, got)
		assert.NotEqual(t, dirY, got, "fail-loud floor must NEVER silently return the cwd tree for a cross-repo arg")
	})

	t.Run("cross_repo_name_resolves_via_manifest", func(t *testing.T) {
		// A bare cross-repo NAME recorded in the machine-local manifest resolves
		// to its recorded directory — the recorded-fact path the ticket adds.
		m := withTestManifest(t)
		parent := t.TempDir()
		dirY := filepath.Join(parent, "knowledge")
		dirX := filepath.Join(parent, "agent")
		require.NoError(t, os.MkdirAll(dirY, 0o750))
		require.NoError(t, os.MkdirAll(dirX, 0o750))
		require.NoError(t, m.Record("agent", dirX))
		deps := astTestDeps{rootDir: dirY}
		got, err := resolveRepoDir(ctx, deps, "agent")
		require.NoError(t, err)
		assert.Equal(t, dirX, got, "a manifest-recorded cross-repo name must resolve to its recorded dir")
	})

	t.Run("cross_repo_manifest_stale_dir_fails_loud", func(t *testing.T) {
		// A manifest entry whose recorded checkout has since been removed must
		// fall through to the fail-loud floor, never walk the phantom path.
		m := withTestManifest(t)
		parent := t.TempDir()
		dirY := filepath.Join(parent, "knowledge")
		require.NoError(t, os.MkdirAll(dirY, 0o750))
		goneDir := filepath.Join(parent, "agent-was-here")
		require.NoError(t, m.Record("agent", goneDir)) // recorded but never created
		deps := astTestDeps{rootDir: dirY}
		got, err := resolveRepoDir(ctx, deps, "agent")
		require.Error(t, err)
		assert.Empty(t, got)
	})

	t.Run("abs_path_walks_existing_dir", func(t *testing.T) {
		// An absolute path to an existing directory is the user's direct
		// instruction: walk it as-is, no sibling probe, no ResolveCwd gate.
		parent := t.TempDir()
		dirY := filepath.Join(parent, "knowledge")
		require.NoError(t, os.MkdirAll(dirY, 0o750))
		absTarget := t.TempDir() // an unrelated existing absolute dir
		deps := astTestDeps{rootDir: dirY}
		got, err := resolveRepoDir(ctx, deps, absTarget)
		require.NoError(t, err)
		assert.Equal(t, absTarget, got, "an existing absolute path must be walked directly")
	})

	t.Run("abs_path_missing_errors", func(t *testing.T) {
		// A non-existent absolute path hits the fail-loud floor — never a
		// silent fallback to the cwd tree.
		parent := t.TempDir()
		dirY := filepath.Join(parent, "knowledge")
		require.NoError(t, os.MkdirAll(dirY, 0o750))
		missing := filepath.Join(t.TempDir(), "does-not-exist")
		deps := astTestDeps{rootDir: dirY}
		got, err := resolveRepoDir(ctx, deps, missing)
		require.Error(t, err)
		assert.Empty(t, got)
	})

	t.Run("empty_root_errors", func(t *testing.T) {
		deps := astTestDeps{rootDir: ""}
		_, err := resolveRepoDir(ctx, deps, "")
		require.Error(t, err)
	})

	t.Run("defaulted_root_omitted_repo_fails_loud", func(t *testing.T) {
		// A DEFAULTED root (rootDirSet: false) with no repo arg and no session cwd
		// (the shared ctx := context.Background() carries none) fails loud rather
		// than silently walking the daemon's process cwd. This carries the
		// defaulted-"." coverage the reframed relative_root subtest used to imply.
		deps := astTestDeps{rootDir: ".", rootDirSet: false}
		got, err := resolveRepoDir(ctx, deps, "")
		require.Error(t, err)
		assert.Empty(t, got)
		assert.Contains(t, err.Error(), "project root")
		assert.Contains(t, err.Error(), "--root")
	})

	t.Run("explicitly_set_root_omitted_repo_preserved", func(t *testing.T) {
		// An EXPLICITLY-set root with no repo arg is preserved even with an empty
		// session cwd — the was-set bit alone keeps the walk.
		realDir := t.TempDir()
		deps := astTestDeps{rootDir: realDir, rootDirSet: true}
		got, err := resolveRepoDir(ctx, deps, "")
		require.NoError(t, err)
		assert.Equal(t, realDir, got)
	})

	t.Run("session_cwd_present_omitted_repo_succeeds", func(t *testing.T) {
		// A live session cwd on ctx preserves the walk even with a DEFAULTED root
		// (rootDirSet: false). This pins the session-cwd dimension of the guard's
		// two-input AND contract: a regression dropping the
		// `if session.WorkspaceCwdFromContext(ctx) == ""` wrapper (so the guard
		// fires on !RootDirSet() alone) makes THIS go RED — the guard would
		// spuriously error despite a live session cwd, silently breaking HTTP
		// interactive use. The LOCAL ctx here SHADOWS the subtest-shared
		// context.Background() at the top of TestResolveRepoDir.
		realDir := t.TempDir()
		ctx := session.ContextWithWorkspaceCwd(context.Background(), realDir)
		deps := astTestDeps{rootDir: ".", rootDirSet: false}
		got, err := resolveRepoDir(ctx, deps, "")
		require.NoError(t, err)
		assert.Equal(t, realDir, got)
	})
}

// TestAstWalkedRootAndZeroScanHint pins the walked_root echo on both count and
// match, and the fires/does-not-fire behavior of the zero-files-scanned hint:
// it fires ONLY when FilesScanned==0 (wrong-root signal), never on
// scanned-but-no-match (which keeps the generic emptyResultHint). deps use
// rootDirSet:true so the omitted-repo walk is preserved (the fail-loud guard
// only trips a DEFAULTED root).
func TestAstWalkedRootAndZeroScanHint(t *testing.T) {
	t.Run("count_scanned_echoes_walked_root_no_hint", func(t *testing.T) {
		repoDir := astFixtureRepo(t)
		deps := astTestDeps{rootDir: repoDir, rootDirSet: true}
		body, isErr, _ := callAst(t, deps, `{"operation":"count","language":"go","pattern":"func $NAME() {}"}`)
		require.False(t, isErr, "count failed: %s", body)
		var out struct {
			WalkedRoot   string `json:"walked_root"`
			Hint         string `json:"hint"`
			FilesScanned int    `json:"files_scanned"`
		}
		require.NoError(t, json.Unmarshal([]byte(body), &out))
		assert.Equal(t, repoDir, out.WalkedRoot)
		assert.Positive(t, out.FilesScanned)
		assert.Empty(t, out.Hint, "no hint when files were scanned")
	})

	t.Run("count_zero_scanned_fires_wrong_root_hint", func(t *testing.T) {
		emptyDir := t.TempDir() // no .go files → FilesScanned==0
		deps := astTestDeps{rootDir: emptyDir, rootDirSet: true}
		body, isErr, _ := callAst(t, deps, `{"operation":"count","language":"go","pattern":"func $NAME() {}"}`)
		require.False(t, isErr, "count failed: %s", body)
		var out struct {
			Hint         string `json:"hint"`
			FilesScanned int    `json:"files_scanned"`
		}
		require.NoError(t, json.Unmarshal([]byte(body), &out))
		assert.Equal(t, 0, out.FilesScanned)
		assert.Contains(t, out.Hint, "wrong root")
		assert.Contains(t, out.Hint, emptyDir)
		assert.Contains(t, out.Hint, "go")
	})

	t.Run("match_scanned_echoes_walked_root", func(t *testing.T) {
		repoDir := astFixtureRepo(t)
		deps := astTestDeps{rootDir: repoDir, rootDirSet: true}
		body, isErr, _ := callAst(t, deps, `{"operation":"match","language":"go","pattern":"func $NAME() {}"}`)
		require.False(t, isErr, "match failed: %s", body)
		var out struct {
			WalkedRoot string `json:"walked_root"`
		}
		require.NoError(t, json.Unmarshal([]byte(body), &out))
		assert.Equal(t, repoDir, out.WalkedRoot)
	})

	t.Run("match_zero_scanned_uses_wrong_root_hint_not_empty_result", func(t *testing.T) {
		emptyDir := t.TempDir() // no .go files → FilesScanned==0
		deps := astTestDeps{rootDir: emptyDir, rootDirSet: true}
		body, isErr, _ := callAst(t, deps, `{"operation":"match","language":"go","pattern":"func $NAME() {}"}`)
		require.False(t, isErr, "match failed: %s", body)
		var out struct {
			WalkedRoot string `json:"walked_root"`
			Hint       string `json:"hint"`
			Stats      struct {
				FilesScanned int `json:"files_scanned"`
			} `json:"stats"`
		}
		require.NoError(t, json.Unmarshal([]byte(body), &out))
		assert.Equal(t, 0, out.Stats.FilesScanned)
		assert.Equal(t, emptyDir, out.WalkedRoot)
		assert.Contains(t, out.Hint, "wrong root", "zero-scan match must carry the wrong-root hint")
		assert.NotContains(t, out.Hint, "no matches", "must NOT be the scanned-but-no-match emptyResultHint")
	})

	t.Run("match_scanned_no_match_keeps_empty_result_hint", func(t *testing.T) {
		// Files WERE scanned but nothing matched → keep the generic emptyResultHint,
		// NOT the wrong-root text. Pins the fires/does-not-fire distinction.
		repoDir := astFixtureRepo(t)
		deps := astTestDeps{rootDir: repoDir, rootDirSet: true}
		body, isErr, _ := callAst(t, deps, `{"operation":"match","language":"go","pattern":"func ZZZNoSuchName() {}"}`)
		require.False(t, isErr, "match failed: %s", body)
		var out struct {
			Hint  string `json:"hint"`
			Stats struct {
				FilesScanned int `json:"files_scanned"`
			} `json:"stats"`
		}
		require.NoError(t, json.Unmarshal([]byte(body), &out))
		assert.Positive(t, out.Stats.FilesScanned)
		assert.Contains(t, out.Hint, "no matches")
		assert.NotContains(t, out.Hint, "wrong root", "scanned-but-no-match must NOT use the wrong-root hint")
	})
}
