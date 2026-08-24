// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// TestLoadL2Only_OSSPath pins the Phase-3 L2-only load(): on the OSS-local source
// path a genuinely-cold L2 returns nil, imports nothing, sets the l2Loaded guard,
// and issues ZERO server RPC (the localSegmentSource makes no network call by
// construction); over a WARM L2 the same load imports the resident set from disk.
func TestLoadL2Only_OSSPath(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	t.Run("empty L2: load returns nil, imports nothing, local source (no server Fetch)", func(t *testing.T) {
		mgr := closeOnCleanup(t, NewManager(loginStateStub{loggedIn: false}, t.TempDir(), 0))
		dm := mgr.managerFor(kgtypes.GraphCode, "empty")
		require.IsType(t, (*localSegmentSource)(nil), dm.source, "OSS caller selects the local source")

		require.NoError(t, dm.load(ctx), "cold-L2 OSS load returns nil (nothing to recover FROM)")
		require.True(t, dm.l2Loaded.Load(), "load sets the l2Loaded guard even on a cold cache")
		require.Equal(t, 0, dm.engine.ResidentDocCount(), "empty L2 imports nothing")
	})

	t.Run("warm L2: load imports the resident set from disk with zero network", func(t *testing.T) {
		dir := t.TempDir()
		// Warm the shared L2 dir via an OSS producer Manager: the write + tick seal a
		// real HNSW segment and warm the L2 cache (shipNew cache.Put) with zero network.
		prod := closeOnCleanup(t, NewManager(loginStateStub{loggedIn: false}, dir, 0))
		seedShipped(t, ctx, prod, kgtypes.GraphCode, "warm", hnswVecDocs(96))
		require.NoError(t, prod.Flush(ctx, kgtypes.GraphCode, "warm"))

		// A FRESH consumer Manager at the SAME dir loads from L2 alone.
		cons := closeOnCleanup(t, NewManager(loginStateStub{loggedIn: false}, dir, 0))
		dm := cons.managerFor(kgtypes.GraphCode, "warm")
		require.IsType(t, (*localSegmentSource)(nil), dm.source, "OSS caller selects the local source")
		require.NoError(t, dm.load(ctx))
		require.True(t, dm.l2Loaded.Load())
		require.Equal(t, 96, dm.engine.ResidentDocCount(), "warm L2 imports the resident set from disk")
	})
}

// TestManagerAccessors_L2AuthoritativeAndLoadResidentDocCount pins the two Phase-3
// accessors the bootstrap heal path composes: IsL2Authoritative reports the source
// mode per graph, and LoadResidentDocCount loads L2 then returns the resident count.
func TestManagerAccessors_L2AuthoritativeAndLoadResidentDocCount(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// IsL2Authoritative: true for a not-logged-in caller, false for a logged-in
	// caller (whose source is the GCS source / errorSegmentSource sentinel).
	ossMgr := closeOnCleanup(t, NewManager(loginStateStub{loggedIn: false}, t.TempDir(), 0))
	require.True(t, ossMgr.IsL2Authoritative(kgtypes.GraphCode, "g"),
		"a not-logged-in Manager is L2-authoritative")
	cloudMgr := closeOnCleanup(t, NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0))
	require.False(t, cloudMgr.IsL2Authoritative(kgtypes.GraphCode, "g"),
		"a logged-in Manager is not L2-authoritative")

	// LoadResidentDocCount over an OSS Manager with a WARM L2 returns the L2 resident
	// count, matching ResidentDocCount after the load.
	dir := t.TempDir()
	prod := closeOnCleanup(t, NewManager(loginStateStub{loggedIn: false}, dir, 0))
	seedShipped(t, ctx, prod, kgtypes.GraphCode, "warm", hnswVecDocs(80))
	require.NoError(t, prod.Flush(ctx, kgtypes.GraphCode, "warm"))

	cons := closeOnCleanup(t, NewManager(loginStateStub{loggedIn: false}, dir, 0))
	got, err := cons.LoadResidentDocCount(ctx, kgtypes.GraphCode, "warm")
	require.NoError(t, err)
	require.Equal(t, 80, got, "LoadResidentDocCount loads L2 and returns the resident count")
	require.Equal(t, got, cons.ResidentDocCount(kgtypes.GraphCode, "warm"),
		"after the load, the raw ResidentDocCount matches")
}
