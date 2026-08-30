// SPDX-License-Identifier: Apache-2.0

package coderun

import (
	"context"
	"os/exec"
	"slices"
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

// BranchExists reports whether repoDir has a LOCAL branch named branch.
//
// THE THREE OUTCOMES ARE KEPT DISTINCT, and that is the whole point of the
// helper. (true, nil) the ref is there; (false, nil) the repository was read and
// does not have it; (false, err) the repository could NOT be read, so no
// membership claim can be made about it at all. A caller that collapses the
// third into the second reports "no such branch" about a directory it never
// managed to look inside.
//
// THE COMPARISON IS EXACT BECAUSE THE PATTERN IS NOT. for-each-ref matches a
// pattern at component boundaries, so refs/heads/feature matches the ref
// feature/x — measured. Reporting that as "feature exists" would accept a branch
// name nobody has. The pattern narrows the scan; the equality decides.
//
// A missing ref is NOT an error to git here: for-each-ref exits 0 and prints
// nothing, which is what lets absence and unreadability stay separable.
func BranchExists(ctx context.Context, repoDir, branch string) (bool, error) {
	if branch == "" {
		return false, nil
	}
	cmd := exec.CommandContext(ctx, "git", "-C", repoDir, "for-each-ref", "--format=%(refname:short)", "refs/heads/"+branch)
	out, err := cmd.Output()
	if err != nil {
		return false, err
	}
	return slices.Contains(strings.Split(strings.TrimSpace(string(out)), "\n"), branch), nil
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
