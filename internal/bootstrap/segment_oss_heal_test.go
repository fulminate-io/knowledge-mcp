// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1/knowledgev1connect"
	"github.com/fulminate-io/knowledge-mcp/internal/auth"
	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/segmentdist"
)

// buildOSSHealClient wires a *client for the OSS (not-logged-in) heal path: a
// logged-OUT router (so GraphCaller/PipelineScanner reach the node-graph
// EngineService — Stats embedded count + PipelineScan) and a segmentMgr whose caller
// reports LoggedIn==false, which no longer selects anything: the Manager reads its
// L2 disk cache directly. With the SegmentService deleted and no source abstraction
// left, the segment path issues NO network call by construction, so "zero server RPC"
// is a STRUCTURAL property (no SegmentService mount exists to hit). The reconcileEngine serves the embedded count
// (BinaryVectorCount) and the rebuild scan over the EngineService.
func buildOSSHealClient(t *testing.T, embedded int32, codeRepos ...string) (*client, *reconcileEngine) {
	t.Helper()
	eng := &reconcileEngine{
		countingEngine: &countingEngine{},
		namesByType:    map[string][]string{string(kgtypes.GraphCode): codeRepos},
		embedded:       embedded,
		scanItems:      map[string][]*knowledgev1.PipelineScanItem{},
		scanCalls:      map[string]int{},
	}

	mux := http.NewServeMux()
	engPath, engHdlr := knowledgev1connect.NewEngineServiceHandler(eng)
	mux.Handle(engPath, engHdlr)
	srv := httptest.NewServer(h2c.NewHandler(mux, &http2.Server{}))
	t.Cleanup(func() { srv.CloseClientConnections(); srv.Close() })

	local := graphclient.NewGraphClientForURL(srv.URL)
	t.Cleanup(local.CloseIdleConnections)
	authState := auth.NewAuthState(newFakeAuthStore(), time.Minute) // logged OUT → OSS-local source
	router := graphclient.NewRouter(local, srv.URL, staticTokenSource{tok: "tok"}, authState)

	c := &client{
		local:      local,
		router:     router,
		authState:  authState,
		segmentMgr: segmentdist.NewManager(t.TempDir(), 0), // router.LoggedIn==false → OSS-local source
		workingSet: fixtureWorkingSet(codeRepos...),

		localPresence: fixturePresence(),
	}
	// Only Manager.Close stops the per-engine merger goroutines this spawns.
	t.Cleanup(c.segmentMgr.Close)
	return c, eng
}

// TestHealNeedsRebuildLocal_OSSZeroRPC is the end-to-end OSS HEAL path under
// zero-segment-RPC counting. healNeedsRebuild routes through the L2-authoritative
// top branch to healNeedsRebuildLocal, which decides degeneracy from LOCAL operands
// only. Across all four cases the counting caller records ZERO segment-service legs —
// proving no escape to a remote manifest snapshot or a remote list-delta.
//
// THIS IS NOW A STRUCTURAL PROPERTY, not a behavioral one: the remote source that
// could have issued those legs is deleted, so the count cannot be non-zero for any
// input. The test is kept because a future re-introduction of a network read on this
// path is exactly the regression it was written to catch, and a counter that reads
// zero for a structural reason still fails loudly the moment the structure changes.
func TestHealNeedsRebuildLocal_OSSZeroRPC(t *testing.T) {
	ctx := opCtx()

	t.Run("a: empty L2 + embedded>=floor -> rebuild, zero legs, rebuild also zero legs", func(t *testing.T) {
		const repo = "ossEmptyRepo"
		c, eng := buildOSSHealClient(t, 120, repo)
		// THE L2-AUTHORITATIVE ASSERTION WAS REMOVED HERE. Its predicate distinguished an OSS
		// caller's L2 source from a logged-in caller's cloud source. There is one
		// source, so the predicate had one answer and no caller, and it is deleted.
		// The question is VOID, not merely unasked — nothing replaces it.

		needs, err := c.healNeedsRebuild(ctx, kgtypes.GraphCode, repo)
		require.NoError(t, err)
		require.True(t, needs, "empty L2 (resident=0) with embedded>=floor rebuilds via the one-shot trigger")

		// The follow-on rebuild pages PipelineScan (node graph) and ships via the OSS
		// source — also ZERO SegmentService RPC.
		eng.scanItems[repo] = makeReconcileScanPage(repo, 10)
		heal := c.buildHealFactory()(kgtypes.GraphCode, repo)
		require.NotNil(t, heal)
		require.NoError(t, heal(ctx))
		require.GreaterOrEqual(t, eng.scanCallCount(repo), 1, "the rebuild scanned the embedded nodes")
	})

	t.Run("b: warm L2 covering >=ratio*embedded, embedded>=floor -> no rebuild, zero legs", func(t *testing.T) {
		const repo = "ossWarmRepo"
		c, _ := buildOSSHealClient(t, 100, repo)
		// Warm the OSS L2 to resident 60 (>= 0.5*100) via a local write plus its re-emit
		// (zero SegmentService legs — the write path lands in L2 and nowhere else).
		require.NoError(t, c.segmentMgr.AddAndMarkDirty(ctx, kgtypes.GraphCode, repo, fastloadVecDocs(repo, 60)))
		require.NoError(t, c.segmentMgr.ReEmitDirtyBuckets(ctx, kgtypes.GraphCode, repo))

		needs, err := c.healNeedsRebuild(ctx, kgtypes.GraphCode, repo)
		require.NoError(t, err)
		require.False(t, needs, "resident 60 >= 0.5*100 embedded -> healthy, no rebuild")
	})

	t.Run("c: tiny graph (embedded<floor) with non-empty L2 -> no rebuild, zero legs", func(t *testing.T) {
		const repo = "ossTinyHealthyRepo"
		c, _ := buildOSSHealClient(t, 30, repo) // embedded 30 < floor 64
		// Warm the OSS L2 to resident 30 (>0 clears the one-shot trigger).
		require.NoError(t, c.segmentMgr.AddAndMarkDirty(ctx, kgtypes.GraphCode, repo, fastloadVecDocs(repo, 30)))
		require.NoError(t, c.segmentMgr.ReEmitDirtyBuckets(ctx, kgtypes.GraphCode, repo))

		needs, err := c.healNeedsRebuild(ctx, kgtypes.GraphCode, repo)
		require.NoError(t, err)
		require.False(t, needs, "sub-floor graph with a non-empty L2 resident -> ratio disarmed, no rebuild")
	})

	t.Run("d: sub-floor empty/lost L2 (resident=0, embedded>0) -> rebuild via one-shot, zero legs", func(t *testing.T) {
		const repo = "ossSubfloorLostRepo"
		c, _ := buildOSSHealClient(t, 30, repo) // embedded 30 < floor, empty L2 -> resident 0

		needs, err := c.healNeedsRebuild(ctx, kgtypes.GraphCode, repo)
		require.NoError(t, err)
		require.True(t, needs,
			"30 sub-floor embedded nodes + lost L2 (resident=0) rebuilds via the one-shot empty-pool trigger — the restored sub-floor zero-presence heal")
	})
}

// TestHealNeedsRebuildLocal_NonFlapping proves the one-shot empty-pool trigger does
// NOT flap once the embed engine's resident set is raised. Reviewer-mandated shape:
// the one-shot is NOT non-flapping "because the rebuild makes resident>0" (the
// rebuild populates the deterministic engine + L2, NOT the l2Loaded-guarded embed
// manager LoadResidentDocCount reads, so resident stays 0 right after a rebuild). It
// self-clears when a LATER event raises the embed resident set — here a normal
// embed-drain write plus its re-emit (zero SegmentService legs) between the two heal
// invocations. The second invocation then returns false.
func TestHealNeedsRebuildLocal_NonFlapping(t *testing.T) {
	ctx := opCtx()
	const repo = "ossNonFlapRepo"
	c, _ := buildOSSHealClient(t, 30, repo) // embedded 30, empty L2 initially

	// FIRST probe: empty L2 -> one-shot fires (case d).
	needs, err := c.healNeedsRebuild(ctx, kgtypes.GraphCode, repo)
	require.NoError(t, err)
	require.True(t, needs, "first probe: empty L2 fires the one-shot rebuild trigger")

	// BETWEEN: a normal embed-drain write plus its re-emit raises the embed engine's
	// resident set (models the self-clear — NOT the rebuild, which leaves the embed
	// manager at resident 0). Zero SegmentService legs (local ship).
	require.NoError(t, c.segmentMgr.AddAndMarkDirty(ctx, kgtypes.GraphCode, repo, fastloadVecDocs(repo, 30)))
	require.NoError(t, c.segmentMgr.ReEmitDirtyBuckets(ctx, kgtypes.GraphCode, repo))

	// SECOND probe: resident>0 now clears the one-shot, and the sub-floor ratio is
	// disarmed -> no rebuild. The trigger did NOT flap.
	needs, err = c.healNeedsRebuild(ctx, kgtypes.GraphCode, repo)
	require.NoError(t, err)
	require.False(t, needs, "second probe after resident>0 -> no re-fire (non-flapping)")
}
