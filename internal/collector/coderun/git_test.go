// SPDX-License-Identifier: Apache-2.0

package coderun

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// gitInitFixture creates a temp directory, runs `git init`, configures a
// throwaway author identity (so the test process doesn't depend on the
// developer's ~/.gitconfig), and returns the directory. Skips the test
// when `git` is not on PATH — same posture as other code-graph tests.
func gitInitFixture(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	mustRun(t, dir, "init", "--initial-branch=main")
	mustRun(t, dir, "config", "user.email", "test@example.invalid")
	mustRun(t, dir, "config", "user.name", "test")
	mustRun(t, dir, "config", "commit.gpgsign", "false")
	return dir
}

func mustRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
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
	mustRun(t, dir, "commit", "-m", message)
	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	require.NoError(t, err)
	return string(out)
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
