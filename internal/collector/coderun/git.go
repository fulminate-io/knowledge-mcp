// SPDX-License-Identifier: Apache-2.0

package coderun

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
)

// DetectBranch returns the current git branch name for the given repo directory,
// exactly as `git rev-parse --abbrev-ref HEAD` reports it.
//
// A detached HEAD is NOT an error at this layer, and that is git's own semantics
// rather than a choice made here: the command exits 0 and prints the literal
// string "HEAD", so this returns ("HEAD", nil). Deciding whether an
// undetermined branch matters belongs to the caller, which is where the manifest
// and overlay context needed to judge it lives. Only a genuine git error — the
// directory missing, not a git repo, no commits yet — returns a non-nil error,
// and the branch is empty in that case.
func DetectBranch(ctx context.Context, repoDir string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", repoDir, "rev-parse", "--abbrev-ref", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// HeadCommit returns the current HEAD commit SHA for the given repo directory.
// An empty SHA is returned alongside the error (not a git repo, no commits yet,
// etc.).
// Shifted client-side off the deleted server-side
// fetchGitInfoIfNeeded — clients pass the SHA to the server via the
// staleness wire args.
func HeadCommit(ctx context.Context, repoDir string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", repoDir, "rev-parse", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// UncommittedCount returns the number of files with uncommitted changes
// (tracked-but-modified). Untracked files are excluded — matches the
// prior server-side `git diff --name-only` semantics. Returns 0 on
// error so callers can treat non-git cwds as "no uncommitted work".
func UncommittedCount(ctx context.Context, repoDir string) (int, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", repoDir, "diff", "--name-only")
	out, err := cmd.Output()
	if err != nil {
		return 0, err
	}
	trimmed := strings.TrimSpace(string(out))
	if trimmed == "" {
		return 0, nil
	}
	return strings.Count(trimmed, "\n") + 1, nil
}

// CommitsBehind returns the number of commits between syncCommit (the
// commit recorded when the index was last refreshed) and HEAD.
// Empty syncCommit short-circuits to 0; rev-list errors surface to the
// caller (likely "unknown revision") so the client can elide the field
// rather than send a misleading zero.
func CommitsBehind(ctx context.Context, repoDir, syncCommit string) (int, error) {
	if syncCommit == "" {
		return 0, nil
	}
	cmd := exec.CommandContext(ctx, "git", "-C", repoDir, "rev-list", "--count", syncCommit+"..HEAD")
	out, err := cmd.Output()
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return 0, err
	}
	return n, nil
}
