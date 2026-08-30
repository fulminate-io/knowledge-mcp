// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// intercept_repo_branch_validation_test.go — a caller-supplied branch must name
// something a read can actually be scoped to.
//
// THE STATE THIS EXISTS TO STOP is not a crash: an unknown branch is answered
// server-side from the BASE graph and rendered under a header naming the branch
// the caller asked for, so the caller receives a plausible payload about a branch
// that does not exist. Nothing downstream can tell that from a real branch read.
//
// THE PROBE ARMS ARE THE LOAD-BEARING ONES. "The repository says no" and "the
// repository could not be read" are different answers, and collapsing the second
// into the first states a membership nobody observed. Collapsing it the other way
// — accepting when a probe fails — is a silent fallback. Both are covered.

// gitBranchFixture builds on the package's gitRepoFixture by creating extra
// local branches, so a subtest can supply a branch that genuinely exists.
func gitBranchFixture(t *testing.T, branches ...string) string {
	t.Helper()
	dir := gitRepoFixture(t)
	for _, b := range branches {
		cmd := exec.Command("git", "-C", dir, "branch", b)
		cmd.Env = hermeticGitEnv()
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git branch %s: %v\n%s", b, err, out)
		}
	}
	return dir
}

// searchWithBranch is the call shape under test: a code-graph search carrying an
// explicit repo and branch.
func searchWithBranch(repo, branch string) kgtools.CallToolParams {
	return paramsFor("search", `{"query":"x","repo":"`+repo+`","branch":"`+branch+`"}`)
}

// branchRefusalText is the refusal a handled call carried. It REQUIRES a content
// block: a handled call that returned nothing would otherwise let every
// Contains/NotContains assertion below pass against an empty string.
func branchRefusalText(t *testing.T, r kgtools.ToolResult) string {
	t.Helper()
	require.NotEmpty(t, r.Content, "a refused call must carry its reason")
	return r.Content[0].Text
}

func TestInjectRepoIfCodeGraph_CallerSuppliedBranchMustExist(t *testing.T) {
	t.Run("local_ref_accepted", func(t *testing.T) {
		m := withTestManifest(t)
		dir := gitBranchFixture(t, "feature-x")
		repo := filepath.Base(dir)
		require.NoError(t, m.Record(repo, dir))

		// NO GraphCaller is wired. That is deliberate: a git hit must ACCEPT
		// without consulting the branch-graph list at all, so a run that reached
		// the RPC would error here rather than pass quietly.
		deps := &repoTestDeps{rootDir: t.TempDir()}

		out, handled, res := InjectRepoIfCodeGraph(context.Background(), deps, searchWithBranch(repo, "feature-x"))
		require.False(t, handled, "a real local branch must be accepted: %v", res)
		assert.Equal(t, "feature-x", callArgs(t, out.Arguments)["branch"])
	})

	t.Run("registered_overlay_accepted", func(t *testing.T) {
		m := withTestManifest(t)
		dir := gitBranchFixture(t) // main only — the branch is NOT a local ref
		repo := filepath.Base(dir)
		require.NoError(t, m.Record(repo, dir))

		deps := &repoTestDeps{rootDir: t.TempDir(), gc: &fakeIndexer{
			branches: []*knowledgev1.GraphInfo{{Name: "collected-only"}},
		}}

		out, handled, res := InjectRepoIfCodeGraph(context.Background(), deps, searchWithBranch(repo, "collected-only"))
		require.False(t, handled,
			"a branch with no local ref but a collected branch graph must still be readable: %v", res)
		assert.Equal(t, "collected-only", callArgs(t, out.Arguments)["branch"])
	})

	t.Run("unknown_branch_refused", func(t *testing.T) {
		m := withTestManifest(t)
		dir := gitBranchFixture(t)
		repo := filepath.Base(dir)
		require.NoError(t, m.Record(repo, dir))

		// The branch-graph list is READABLE and non-empty: both vocabularies were
		// consulted and neither holds the branch, which is what makes this bad
		// input rather than an unverifiable probe.
		deps := &repoTestDeps{rootDir: t.TempDir(), gc: &fakeIndexer{
			branches: []*knowledgev1.GraphInfo{{Name: "some-other-branch"}},
		}}

		_, handled, res := InjectRepoIfCodeGraph(context.Background(), deps, searchWithBranch(repo, "no-such-branch"))
		require.True(t, handled, "an unknown branch must be refused, not answered from the base graph")
		msg := branchRefusalText(t, res)
		assert.Contains(t, msg, "is not a branch of repo")
		assert.Contains(t, msg, "no branch graph of that name exists")
		assert.Contains(t, msg, "some-other-branch",
			"the refusal must name the branch graphs that ARE available, or it states a rejection without the accepted set")
	})

	t.Run("auto_detected_branch_not_validated", func(t *testing.T) {
		m := withTestManifest(t)
		dir := gitBranchFixture(t)
		repo := filepath.Base(dir)
		require.NoError(t, m.Record(repo, dir))

		// NEGATIVE CONTROL FOR SCOPE. No GraphCaller is wired, so if this path ever
		// reached the validation the branch-graph read would fail and the call would
		// be refused. It must not: an auto-detected branch came from git by
		// construction and re-checking it would spend an exec and an RPC to
		// re-derive a fact this package just produced.
		deps := &repoTestDeps{rootDir: t.TempDir()}

		out, handled, res := InjectRepoIfCodeGraph(context.Background(), deps,
			paramsFor("search", `{"query":"x","repo":"`+repo+`"}`))
		require.False(t, handled, "an auto-detected branch must never be validated: %v", res)
		assert.Equal(t, "main", callArgs(t, out.Arguments)["branch"])
	})

	t.Run("overlay_list_unreadable_errors", func(t *testing.T) {
		m := withTestManifest(t)
		dir := gitBranchFixture(t)
		repo := filepath.Base(dir)
		require.NoError(t, m.Record(repo, dir))

		deps := &repoTestDeps{rootDir: t.TempDir(), gc: &fakeIndexer{
			indexErr: assert.AnError,
		}}

		_, handled, res := InjectRepoIfCodeGraph(context.Background(), deps, searchWithBranch(repo, "unknown"))
		require.True(t, handled, "an unreadable branch-graph list must not be accepted — that would be a silent fallback")
		msg := branchRefusalText(t, res)
		assert.Contains(t, msg, "cannot be verified")
		assert.NotContains(t, msg, "is not a branch of repo",
			"a probe that failed says nothing about membership; rendering it as a negative result is a false "+
				"explanation of a state nobody observed")
	})

	t.Run("git_probe_unreadable_errors", func(t *testing.T) {
		m := withTestManifest(t)
		// A recorded directory that EXISTS but is not a git repository: the probe
		// fails, as distinct from a manifest MISS, where there is simply no
		// checkout to consult and the branch-graph set decides alone.
		notARepo := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(notARepo, "a.txt"), []byte("x"), 0o600))
		repo := filepath.Base(notARepo)
		require.NoError(t, m.Record(repo, notARepo))

		// The branch-graph list is READABLE and holds the branch. It must NOT
		// rescue the call: the git probe failed, so this client cannot say whether
		// the branch is a local ref, and it refuses rather than guess.
		deps := &repoTestDeps{rootDir: t.TempDir(), gc: &fakeIndexer{
			branches: []*knowledgev1.GraphInfo{{Name: "unknown"}},
		}}

		_, handled, res := InjectRepoIfCodeGraph(context.Background(), deps, searchWithBranch(repo, "unknown"))
		require.True(t, handled, "an unreadable checkout must not be accepted on the strength of the other vocabulary alone")
		msg := branchRefusalText(t, res)
		assert.Contains(t, msg, "could not be read as a git repository")
		assert.NotContains(t, msg, "is not a branch of repo",
			"a failed git probe must not be reported as a membership answer")
	})
}
