// SPDX-License-Identifier: Apache-2.0

package coderun

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// hermeticGitEnv returns os.Environ() with every GIT_* entry stripped, then
// re-adds GIT_TERMINAL_PROMPT=0. Test fixtures that spawn git subprocesses MUST
// use this instead of raw os.Environ(): inside a worktree or a git hook, git
// exports GIT_DIR / GIT_INDEX_FILE / GIT_WORK_TREE / etc. into child processes,
// and those override `git -C <dir>` and cmd.Dir — so a fixture's `git init`
// would re-init the host worktree gitdir (flipping core.bare=true) and its
// commits would land on the host branch. Scrubbing GIT_* makes the fixture
// operate only in its own temp dir regardless of the ambient env.
func hermeticGitEnv() []string {
	base := os.Environ()
	out := make([]string, 0, len(base)+1)
	for _, kv := range base {
		if strings.HasPrefix(kv, "GIT_") {
			continue
		}
		out = append(out, kv)
	}
	return append(out, "GIT_TERMINAL_PROMPT=0")
}

// gitInitFixture creates a temp directory, runs `git init`, and returns the
// directory. Skips the test when `git` is not on PATH — same posture as other
// code-graph tests.
//
// REASON THIS TEST SPAWNS GIT (approved site, see the git-in-tests allowlist
// under scripts/testdata/): this file is the regression test proving the
// collector's git fixtures are hermetic, so it must build real repositories and
// run real git against them. Every repository is a t.TempDir and every command
// runs under hermeticGitEnv.
func gitInitFixture(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	mustRun(t, dir, "init", "--initial-branch=main")
	return dir
}

// throwawayIdentity is the committer identity for fixture commits, passed
// per-command with -c. It is deliberately NOT written with `git config
// user.name/user.email`: that form persists the value into the target
// repository's config, and under a leaked GIT_DIR the target is the developer's
// own checkout — the exact mechanism that once rewrote a real author identity
// across a shared history. The -c form writes nothing anywhere.
func throwawayIdentity() []string {
	return []string{
		"-c", "user.email=test@example.invalid",
		"-c", "user.name=test",
		"-c", "commit.gpgsign=false",
	}
}

func mustRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = hermeticGitEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func commitFile(t *testing.T, dir, name, body, message string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(p, []byte(body), 0o600))
	mustRun(t, dir, "add", name)
	mustRun(t, dir, append(throwawayIdentity(), "commit", "-m", message)...)
	revParse := exec.Command("git", "-C", dir, "rev-parse", "HEAD")
	revParse.Env = hermeticGitEnv()
	out, err := revParse.Output()
	require.NoError(t, err)
	return string(out)
}

// probeHost runs a git command against an explicit --git-dir with a hermetic
// env (so the probe itself reads the named gitdir, not whatever the ambient
// GIT_DIR points at) and returns combined output + error.
func probeHost(t *testing.T, hostGitDir string, args ...string) (string, error) {
	t.Helper()
	full := append([]string{"--git-dir=" + hostGitDir}, args...)
	cmd := exec.Command("git", full...)
	cmd.Env = hermeticGitEnv()
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// TestGitFixture_HermeticUnderSimulatedWorktreeEnv proves the coderun git
// fixtures (gitInitFixture / commitFile / mustRun) are hermetic: even when the
// ambient env carries a leaked GIT_DIR / GIT_INDEX_FILE pointing at a "host"
// gitdir (exactly what git exports into hooks from inside a worktree), the
// fixtures must operate only in their own temp dir — NOT re-init the host gitdir
// (flipping core.bare=true) and NOT leak commits onto the host branch.
//
// RED without the Phase-1 hermeticGitEnv() scrub: the leaked GIT_DIR overrides
// `git -C <tmp> init`, so gitInitFixture re-inits hostGitDir (core.bare=true)
// and commitFile's commits land on the host branch — both assertions below fail.
func TestGitFixture_HermeticUnderSimulatedWorktreeEnv(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}

	// Build a throwaway "simulated host" working copy as a real, NON-bare repo
	// with one commit, using hermetic git so this setup is itself unaffected by
	// the ambient env.
	hostDir := t.TempDir()
	for _, args := range [][]string{
		{"-C", hostDir, "init", "--initial-branch=main"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Env = hermeticGitEnv()
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("host setup git %v: %v\n%s", args, err, out)
		}
	}
	require.NoError(t, os.WriteFile(filepath.Join(hostDir, "host.txt"), []byte("host"), 0o600))
	for _, args := range [][]string{
		{"-C", hostDir, "add", "host.txt"},
		append([]string{"-C", hostDir},
			append(throwawayIdentity(), "commit", "-m", "host-initial")...),
	} {
		cmd := exec.Command("git", args...)
		cmd.Env = hermeticGitEnv()
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("host setup git %v: %v\n%s", args, err, out)
		}
	}

	hostGitDir := filepath.Join(hostDir, ".git")

	// Capture the simulated host's HEAD + commit count BEFORE running the
	// fixtures, via explicit --git-dir probes.
	hostHEADBefore, err := probeHost(t, hostGitDir, "rev-parse", "HEAD")
	require.NoError(t, err, "host should have a HEAD before the fixtures run")
	hostCountBefore, err := probeHost(t, hostGitDir, "rev-list", "--count", "HEAD")
	require.NoError(t, err)

	// Simulate the leaked hook env: this is what git exports into hooks from
	// inside a worktree. t.Setenv auto-restores at test end.
	t.Setenv("GIT_DIR", hostGitDir)
	t.Setenv("GIT_INDEX_FILE", filepath.Join(hostGitDir, "index"))

	// Exercise the fixtures under that polluted env. With hermetic fixtures
	// these touch only fixtureDir; without the scrub they corrupt hostGitDir.
	fixtureDir := gitInitFixture(t)
	commitFile(t, fixtureDir, "x.txt", "body", "fixture-commit")

	// Assertion 1: core.bare on the simulated host was NOT flipped. `config
	// --get core.bare` exits non-zero when the key is absent (acceptable); a
	// present value must NOT be "true".
	bare, _ := probeHost(t, hostGitDir, "config", "--get", "core.bare")
	assert.NotEqual(t, "true", bare,
		"leaked GIT_DIR flipped host core.bare=true — fixture re-inited the host gitdir")

	// Assertion 2: no fixture commit leaked onto the host branch — HEAD and the
	// commit count are unchanged.
	hostHEADAfter, err := probeHost(t, hostGitDir, "rev-parse", "HEAD")
	require.NoError(t, err)
	assert.Equal(t, hostHEADBefore, hostHEADAfter,
		"a fixture commit leaked onto the simulated host HEAD")
	hostCountAfter, err := probeHost(t, hostGitDir, "rev-list", "--count", "HEAD")
	require.NoError(t, err)
	assert.Equal(t, hostCountBefore, hostCountAfter,
		"the host commit count changed — fixture commits leaked onto the host branch")

	// Assertion 3 (positive): the fixture did its work in its OWN temp dir. The
	// probe runs hermetically (via probeHost) so it reads the fixture's gitdir
	// rather than the leaked ambient GIT_DIR.
	fixtureHEAD, err := probeHost(t, filepath.Join(fixtureDir, ".git"), "rev-parse", "HEAD")
	require.NoError(t, err, "fixture dir should be a working repo with a commit")
	assert.NotEqual(t, hostHEADBefore, fixtureHEAD,
		"fixture HEAD should differ from the host HEAD (fixture committed in its own dir)")
}

func TestHeadCommit_HappyPath(t *testing.T) {
	dir := gitInitFixture(t)
	commitFile(t, dir, "a.txt", "hello", "initial")

	sha, err := HeadCommit(context.Background(), dir)
	require.NoError(t, err)
	assert.Len(t, sha, 40, "git rev-parse HEAD should be a 40-char SHA")
}

func TestHeadCommit_NotGitRepo(t *testing.T) {
	dir := t.TempDir()
	sha, err := HeadCommit(context.Background(), dir)
	require.Error(t, err)
	assert.Empty(t, sha)
}

func TestUncommittedCount_NoChanges(t *testing.T) {
	dir := gitInitFixture(t)
	commitFile(t, dir, "a.txt", "hello", "initial")

	n, err := UncommittedCount(context.Background(), dir)
	require.NoError(t, err)
	assert.Equal(t, 0, n)
}

func TestUncommittedCount_TwoModified(t *testing.T) {
	dir := gitInitFixture(t)
	commitFile(t, dir, "a.txt", "hello", "initial-a")
	commitFile(t, dir, "b.txt", "world", "initial-b")

	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.txt"), []byte("changed"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "b.txt"), []byte("changed-too"), 0o600))

	n, err := UncommittedCount(context.Background(), dir)
	require.NoError(t, err)
	assert.Equal(t, 2, n)
}

func TestUncommittedCount_NotGitRepo(t *testing.T) {
	dir := t.TempDir()
	_, err := UncommittedCount(context.Background(), dir)
	require.Error(t, err)
}

func TestCommitsBehind_Zero(t *testing.T) {
	dir := gitInitFixture(t)
	sha := commitFile(t, dir, "a.txt", "hello", "initial")

	n, err := CommitsBehind(context.Background(), dir, sha[:40])
	require.NoError(t, err)
	assert.Equal(t, 0, n)
}

func TestCommitsBehind_Three(t *testing.T) {
	dir := gitInitFixture(t)
	first := commitFile(t, dir, "a.txt", "hello", "first")
	commitFile(t, dir, "b.txt", "world", "second")
	commitFile(t, dir, "c.txt", "again", "third")
	commitFile(t, dir, "d.txt", "more", "fourth")

	n, err := CommitsBehind(context.Background(), dir, first[:40])
	require.NoError(t, err)
	assert.Equal(t, 3, n)
}

func TestCommitsBehind_EmptySyncCommit(t *testing.T) {
	dir := gitInitFixture(t)
	commitFile(t, dir, "a.txt", "hello", "initial")

	n, err := CommitsBehind(context.Background(), dir, "")
	require.NoError(t, err)
	assert.Equal(t, 0, n)
}

func TestCommitsBehind_NotGitRepo(t *testing.T) {
	dir := t.TempDir()
	_, err := CommitsBehind(context.Background(), dir, "abc123")
	require.Error(t, err)
}
