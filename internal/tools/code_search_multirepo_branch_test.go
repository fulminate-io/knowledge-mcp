// SPDX-License-Identifier: Apache-2.0

// code_search_multirepo_branch_test.go — the per-repo branch detection the
// cross-repo code search fans out. Every test here builds real git checkouts and
// a temp manifest, because the thing under test is precisely the manifest → git
// → selector chain; a fake manifest would assert the mapping against itself.

package tools

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// gitInFixture runs one git command inside a gitRepoFixture checkout under the
// same hermetic environment the fixture was built with.
func gitInFixture(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = hermeticGitEnv()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// detachHead moves an existing gitRepoFixture checkout onto a detached HEAD,
// the state where `git rev-parse --abbrev-ref HEAD` prints the literal string
// "HEAD" and exits 0.
func detachHead(t *testing.T, dir string) {
	t.Helper()
	gitInFixture(t, dir, "checkout", "--detach", "HEAD")
}

// checkoutBranch puts an existing gitRepoFixture checkout on a new named branch.
func checkoutBranch(t *testing.T, dir, branch string) {
	t.Helper()
	gitInFixture(t, dir, "checkout", "-b", branch)
}

// multiRepoBody drives the cross-repo composer over the named repos and returns
// the rendered text body. deps is nil deliberately: the composer only reaches
// ClientDeps to enumerate graphs for repo="all", and every test here names its
// repos explicitly.
func multiRepoBody(t *testing.T, f *codeSearchEngineFake, repos []string) string {
	t.Helper()
	cdeps := cdepsFor(f)
	cdeps.degrade = &searchDegrade{}
	res := composeCodeSearchMultiRepo(context.Background(), nil, cdeps,
		codeSearchArgs{Graph: "code", Repos: repos, Text: "x"},
		[]string{"x"}, nil, 10, true, false)
	return textBodyTools(res)
}

// TestMultiRepoSearchStampsPerRepoBranch is the regression for the gap this work
// closes: the cross-repo fan-out built every per-repo selector with no Branch, so
// a repos:[...] or repo:"all" search never opened an overlay pool on any branch.
//
// The second repo is the load-bearing half. One branch shared across the fan-out
// would still open an overlay and would still look fixed from the first repo
// alone — so the test pins that each repo is searched under ITS OWN branch.
func TestMultiRepoSearchStampsPerRepoBranch(t *testing.T) {
	m := withTestManifest(t)

	feature := gitRepoFixture(t)
	checkoutBranch(t, feature, "feature-x")
	require.NoError(t, m.Record("feature-repo", feature))

	mainline := gitRepoFixture(t) // left on main
	require.NoError(t, m.Record("main-repo", mainline))

	f := &codeSearchEngineFake{}
	_ = multiRepoBody(t, f, []string{"feature-repo", "main-repo"})
	pools := f.requestedPools()

	// KNOWN-POSITIVE CONTROL, first: a fan-out that searched nothing at all would
	// satisfy every "did not request the wrong pool" assertion below by vacuity.
	require.Len(t, pools, 2,
		"both repos must be searched; requested pools were %+v", pools)

	assert.Contains(t, pools, poolReq{base: "feature-repo", overlay: "feature-repo@feature-x"},
		"the repo on a feature branch must open ITS overlay pool; requested pools were %+v", pools)
	assert.Contains(t, pools, poolReq{base: "main-repo", overlay: "main-repo@main"},
		"the second repo must be searched under ITS OWN detected branch; requested pools were %+v", pools)
	assert.NotContains(t, pools, poolReq{base: "main-repo", overlay: "main-repo@feature-x"},
		"no repo may inherit another repo's branch — a single shared branch across the "+
			"fan-out is the design this fix exists to prevent; requested pools were %+v", pools)
}

// TestMultiRepoBranchDegrade pins the degrade partition in BOTH directions. The
// two subtests are each other's control: without the silent one, a change making
// every manifest miss degrade would pass unnoticed and the banner would fire on
// every cross-repo search on any machine holding graphs it never collected
// locally — and a warning that always fires is not a truthful warning.
func TestMultiRepoBranchDegrade(t *testing.T) {
	t.Run("unreadable_degrades", func(t *testing.T) {
		m := withTestManifest(t)
		// A manifest entry pointing at a directory that is not a git repo: the
		// manifest promised a checkout it cannot deliver, so an overlay may exist
		// and we could not find out.
		require.NoError(t, m.Record("broken-repo", t.TempDir()))

		body := multiRepoBody(t, &codeSearchEngineFake{}, []string{"broken-repo"})

		assert.Contains(t, body, searchDegradedMarker,
			"a broken manifest promise must be surfaced, not served as base content silently; "+
				"body was:\n"+body)
		assert.Contains(t, body, "broken-repo",
			"the degrade line must NAME the repo it is about; body was:\n"+body)
	})

	t.Run("manifest_miss_silent", func(t *testing.T) {
		withTestManifest(t) // empty manifest: no entry for the searched repo at all.

		body := multiRepoBody(t, &codeSearchEngineFake{}, []string{"ghost-repo"})

		// KNOWN-POSITIVE CONTROL for the absence assertion below: prove the search
		// actually ran for this repo, so "no marker" cannot mean "nothing happened".
		require.Contains(t, body, "Cross-repo search across ghost-repo",
			"control: the fan-out must have run for this repo; body was:\n"+body)

		assert.NotContains(t, body, searchDegradedMarker,
			"a repo this machine has no checkout of is NOT degraded — the base graph is "+
				"the complete answer and there is no overlay to miss; body was:\n"+body)
	})
}

// TestASTRepoBranchResolution pins the ast caller's branch derivation, which had
// no coverage at all. ast derives its repo as filepath.Base of the walked
// directory and then resolves a branch from that name, so the test asserts that
// same chain — basename → manifest → branch — including the detached case the
// helper now reports as a detection failure.
//
// SCOPE: this pins the DERIVATION ast performs, not the whole ast handler. The
// branch reaches only the hydrator backend, and the handler tolerates a nil graph
// client by returning empty hydration without error, so the branch is not
// observable from the handler's output without a graph client and a new seam.
func TestASTRepoBranchResolution(t *testing.T) {
	m := withTestManifest(t)

	onBranch := gitRepoFixture(t)
	checkoutBranch(t, onBranch, "ast-branch")
	require.NoError(t, m.Record(filepath.Base(onBranch), onBranch))
	assert.Equal(t, "ast-branch", autoDetectBranch(context.Background(), filepath.Base(onBranch)),
		"ast keys the manifest on the walked directory's basename and reads that checkout's branch")

	detached := gitRepoFixture(t)
	detachHead(t, detached)
	require.NoError(t, m.Record(filepath.Base(detached), detached))
	assert.Empty(t, autoDetectBranch(context.Background(), filepath.Base(detached)),
		"a detached checkout leaves ast on the base graph rather than opening a \"HEAD\" overlay")
}

// TestDetachedHeadDegrades pins BOTH halves of the detached-HEAD correction, and
// neither half alone is sufficient. Returning ("HEAD", failed) would satisfy the
// banner half while leaving the single-repo interceptor and ast still stamping
// "HEAD" as though it were a branch; returning "" with no state mapping would
// satisfy the helper half while the fan-out silently served base content.
func TestDetachedHeadDegrades(t *testing.T) {
	t.Run("helper_empty", func(t *testing.T) {
		m := withTestManifest(t)

		detached := gitRepoFixture(t)
		detachHead(t, detached)
		require.NoError(t, m.Record("detached-repo", detached))

		// KNOWN-POSITIVE CONTROL. The same fixture shape on a named branch must
		// detect normally through the same manifest, so the empty answer below
		// reads as "detached" rather than "the fixture or the manifest never
		// worked" — an empty string is what a broken harness returns too.
		attached := gitRepoFixture(t)
		require.NoError(t, m.Record("attached-repo", attached))
		require.Equal(t, "main", autoDetectBranch(context.Background(), "attached-repo"),
			"control: an ordinary checkout must still detect its branch")

		assert.Empty(t, autoDetectBranch(context.Background(), "detached-repo"),
			"a detached checkout must yield an EMPTY branch — git prints the literal "+
				"\"HEAD\" and exits 0, and stamping that as a branch is the bug")
	})

	t.Run("multirepo_degrades", func(t *testing.T) {
		m := withTestManifest(t)
		detached := gitRepoFixture(t)
		detachHead(t, detached)
		require.NoError(t, m.Record("detached-repo", detached))

		body := multiRepoBody(t, &codeSearchEngineFake{}, []string{"detached-repo"})

		assert.Contains(t, body, searchDegradedMarker,
			"a manifest entry whose checkout has no determinable branch must degrade, "+
				"not serve base content silently; body was:\n"+body)
		assert.Contains(t, body, "detached-repo",
			"the degrade line must NAME the repo whose branch could not be determined; "+
				"body was:\n"+body)
	})
}
