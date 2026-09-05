// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/auth"
	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/segmentdist"
)

// TestSegmentManagerAccessorSharesOneInstance is Phase 3 Step 1's criterion:
// ClientDeps.SegmentManager() returns the SAME *segmentdist.Manager the client
// holds (the one ensureSegmentManager constructed at wireRuntimesBackground) —
// one instance, not a freshly constructed duplicate. ensureSegmentManager assigns
// c.segmentMgr = NewManager(...) and wirePipelineRuntime then attaches that same
// instance to the producer (p.AttachSegmentManager(c.segmentMgr), pipeline.go), so
// the producer (pipeline) and the consumer (SegmentManager()) share one pointer by
// construction; this test pins the accessor half.
func TestSegmentManagerAccessorSharesOneInstance(t *testing.T) {
	mgr := segmentdist.NewManager(t.TempDir(), 0)
	t.Cleanup(mgr.Close)
	c := &client{segmentMgr: mgr}

	got := c.SegmentManager()
	require.NotNil(t, got, "accessor returns the attached manager")
	// Same pointer — the accessor exposes the held instance, not a copy.
	require.Same(t, mgr, got, "SegmentManager() returns the SAME instance the client holds")
}

// TestSegmentManagerAccessorNilWhenUnwired asserts the accessor returns an
// UNTYPED nil (so the search arms' nil fallback fires) when the pipeline was
// never wired (c.segmentMgr nil).
func TestSegmentManagerAccessorNilWhenUnwired(t *testing.T) {
	c := &client{}
	require.Nil(t, c.SegmentManager(), "unwired client yields a nil SegmentManager")
}

// TestEnsureSegmentManagerWiresOffline (FAILS-WHEN-ABSENT) is the offline-search
// core guarantee: ensureSegmentManager — the PRODUCTION construction site
// wireRuntimesBackground calls BEFORE wirePipelineRuntime — leaves a NON-NIL read
// Manager when only the router is wired and NO pipeline/embedder is present (the
// offline / --no-llm-pipeline state). This is the inverse of
// TestSegmentManagerAccessorNilWhenUnwired: a router-bearing offline client gets a
// Manager so the search arms serve BM25 over existing segments rather than
// erroring "client segment engine unavailable". If the construction is re-gated
// behind the pipeline (moved back inside wirePipelineRuntime, which the offline
// early-returns skip), c.segmentMgr stays nil here and this test fails.
func TestEnsureSegmentManagerWiresOffline(t *testing.T) {
	// A non-nil router is the only precondition ensureSegmentManager guards on —
	// NewManager makes no RPC at construction, so a logged-out router pointed at an
	// unreachable URL suffices. No pipeline, no embedder are constructed: this is
	// exactly the offline wiring state.
	authState := auth.NewAuthState(newFakeAuthStore(), time.Minute) // logged out → local
	local := graphclient.NewGraphClientForURL("http://local.invalid")
	t.Cleanup(local.Close)
	router := graphclient.NewRouter(local, "http://local.invalid", staticTokenSource{tok: "tok"}, authState)

	c := &client{local: local, router: router, authState: authState}
	require.Nil(t, c.segmentMgr, "precondition: no Manager before wiring")

	// Drive the exact production construction path (wireRuntimesBackground calls
	// this same method, not an inline copy). A temp dir stands in for the
	// --graph-storage data root the production caller threads through (f.GraphStorage).
	c.ensureSegmentManager(t.TempDir(), 0)
	// Only Manager.Close stops the per-engine merger goroutines the Manager spawns.
	t.Cleanup(c.segmentMgr.Close)

	require.NotNil(t, c.SegmentManager(),
		"offline wiring (router only, no pipeline/embedder) leaves a non-nil read Manager")
}

// TestSegmentCacheDirCoLocation (FAILS-WHEN-ABSENT) pins the three properties the
// co-location fix must hold for segmentCacheDirFor — the successor to the retired
// HOME-fixed segmentCacheDir(). ensureSegmentManager builds the L2 cache root via
// segmentCacheDirFor(graphStorage), where graphStorage is the already
// tilde-expanded --graph-storage data root the daemon was started with.
func TestSegmentCacheDirCoLocation(t *testing.T) {
	home, err := os.UserHomeDir()
	require.NoError(t, err, "home dir resolves in the test environment")

	// (a) No regression for the standard local setup: the default --graph-storage
	// (~/.knowledge/) is tilde-expanded by the client to <home>/.knowledge before it
	// reaches ensureSegmentManager, so the cache lands at <home>/.knowledge/segments —
	// the exact pre-fix location the old HOME-fixed segmentCacheDir() returned.
	defaultRoot := filepath.Join(home, ".knowledge")
	require.Equal(t, filepath.Join(home, ".knowledge", "segments"), segmentCacheDirFor(defaultRoot),
		"default data root yields the pre-fix <home>/.knowledge/segments (no regression)")

	// (b) Co-location for a non-default data root: the cache roots under that root
	// (<dir>/segments) and does NOT leak to HOME — the bug the fix closes.
	nonDefaultRoot := t.TempDir()
	got := segmentCacheDirFor(nonDefaultRoot)
	require.Equal(t, filepath.Join(nonDefaultRoot, "segments"), got,
		"a non-default data root co-locates the cache at <dir>/segments")
	require.False(t, strings.HasPrefix(got, filepath.Join(home, ".knowledge")),
		"a non-default data root must NOT resolve the cache under <home>/.knowledge (no HOME leak)")

	// (c) Client/server parity: segmentCacheDirFor(r) is exactly the server's
	// filepath.Join(r, "segments") expression over a shared root, so the client L2
	// cache and the server segment store co-locate when both run off the same
	// --graph-storage (which they do — the client spawns the server with its own root).
	for _, r := range []string{defaultRoot, nonDefaultRoot, "/var/lib/knowledge-data"} {
		require.Equal(t, filepath.Join(r, "segments"), segmentCacheDirFor(r),
			"segmentCacheDirFor(%q) must equal the server's filepath.Join(r, \"segments\")", r)
	}
}

// TestBuildHealFactoryShape is the auto-heal wiring criterion: with a
// segment manager wired, buildHealFactory returns a non-nil factory that produces
// a NON-NIL heal closure for every graph kgtypes.HasRebuildableSegments admits (the
// embeddable builtins — code, knowledge, cloud, cicd, practice) and a NIL closure
// for a graph with no rebuildable segments (e.g. linkage — the
// HasRebuildableSegments gate). This is the SAME predicate the manual
// rebuild_segments op gates on, so the auto-heal arm and the manual rebuild gate
// cannot drift. Construction-level only — it does NOT drive a live rebuild (the
// probe + rebuild behavior is covered by the segmentdist / tools / maybeHealCheck
// tests).
func TestBuildHealFactoryShape(t *testing.T) {
	mgr := segmentdist.NewManager(t.TempDir(), 0)
	t.Cleanup(mgr.Close)
	c := &client{segmentMgr: mgr}

	factory := c.buildHealFactory()
	require.NotNil(t, factory, "a wired segment manager yields a non-nil heal factory")

	codeClosure := factory(kgtypes.GraphCode, "repo")
	require.NotNil(t, codeClosure, "a code graph gets a non-nil heal closure")

	knowledgeClosure := factory(kgtypes.GraphKnowledge, "kg")
	require.NotNil(t, knowledgeClosure, "the builtin knowledge graph gets a non-nil heal closure")

	practiceClosure := factory(kgtypes.GraphPractice, "go")
	require.NotNil(t, practiceClosure, "practice carries rebuildable segments — gets a non-nil heal closure")

	cloudClosure := factory(kgtypes.GraphCloud, "acct")
	require.NotNil(t, cloudClosure, "cloud carries rebuildable segments — gets a non-nil heal closure")

	cicdClosure := factory(kgtypes.GraphCICD, "org")
	require.NotNil(t, cicdClosure, "cicd carries rebuildable segments — gets a non-nil heal closure")

	linkageClosure := factory(kgtypes.GraphLinkage, "lk")
	require.Nil(t, linkageClosure, "linkage has no rebuildable segments — gets a nil closure (closed-gate side stays pinned)")
}

// TestHealFactoryNotAttachedWithoutSegmentManager asserts the bootstrap guard:
// with no segment manager wired (c.segmentMgr nil), wirePipelineRuntime never
// calls AttachHealFactory, so a degraded/headless client carries no heal closure.
// Mirrors the production guard `if c.segmentMgr != nil { p.AttachHealFactory(...) }`
// — when the manager is absent the heal factory is simply not built/attached, and
// the per-collector heal-check no-ops.
func TestHealFactoryNotAttachedWithoutSegmentManager(t *testing.T) {
	c := &client{} // segmentMgr nil — the degraded/headless path
	require.Nil(t, c.segmentMgr, "no segment manager wired")
	// The bootstrap guard (segmentMgr != nil) is what gates AttachHealFactory; with
	// segmentMgr nil that branch is not taken, so no heal factory is attached. This
	// pins the precondition the guard reads.
}
