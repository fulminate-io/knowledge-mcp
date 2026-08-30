// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// TestLoadL2Only_OSSPath pins the L2-only load(): a genuinely-cold L2 returns nil,
// imports nothing and sets the l2Loaded guard, while a WARM L2 imports the resident
// set from disk. Zero server RPC is structural rather than asserted — the load reads
// the disk cache directly and there is no source, and therefore no network leg, on
// this path at all.
func TestLoadL2Only_OSSPath(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	t.Run("empty L2: load returns nil, imports nothing, local source (no server Fetch)", func(t *testing.T) {
		mgr := closeOnCleanup(t, NewManager(t.TempDir(), 0))
		dm := mgr.managerFor(kgtypes.GraphCode, "empty")
		// THE "WHICH SOURCE DID IT SELECT" ASSERTION IS GONE WITH THE SOURCE. It
		// checked that the constructed distManager held a local source rather than a
		// cloud one; there is no source of any kind, so the question has no referent.
		// What remains is the behaviour it was a proxy for, asserted directly below:
		// the load reads L2 and nothing else.
		require.NoError(t, dm.load(ctx), "cold-L2 OSS load returns nil (nothing to recover FROM)")
		require.True(t, dm.l2Loaded.Load(), "load sets the l2Loaded guard even on a cold cache")
		require.Equal(t, 0, dm.engine.ResidentDocCount(), "empty L2 imports nothing")
	})

	t.Run("warm L2: load imports the resident set from disk with zero network", func(t *testing.T) {
		dir := t.TempDir()
		// Warm the shared L2 dir via an OSS producer Manager: the write + tick seal a
		// real HNSW segment and warm the L2 cache (writeNewBlobsToL2 cache.Put) with
		// zero network.
		prod := closeOnCleanup(t, NewManager(dir, 0))
		seedShipped(t, ctx, prod, kgtypes.GraphCode, "warm", hnswVecDocs(96))
		require.NoError(t, prod.Flush(ctx, kgtypes.GraphCode, "warm"))

		// A FRESH consumer Manager at the SAME dir loads from L2 alone.
		cons := closeOnCleanup(t, NewManager(dir, 0))
		dm := cons.managerFor(kgtypes.GraphCode, "warm")
		require.NoError(t, dm.load(ctx))
		require.True(t, dm.l2Loaded.Load())
		require.Equal(t, 96, dm.engine.ResidentDocCount(), "warm L2 imports the resident set from disk")
	})
}

// TestManagerLoadResidentDocCount pins the accessor the bootstrap heal path composes:
// LoadResidentDocCount loads L2 then returns the resident count.
//
// THE L2-AUTHORITATIVE HALF WAS DELETED, and it was worse than merely dead. It
// asserted that two managers — constructed by the SAME call, NewManager(t.TempDir(),
// 0) — reported opposite modes, because the mode used to come from a caller argument
// the constructor no longer takes. Once the login-state operand was removed there was nothing left
// to make the two differ, so the assertion could only have been read as true by
// nobody running it. The accessor itself is gone with the second mode: every Manager
// is L2-authoritative now, so a predicate answering "is it?" has one answer and no
// caller. No successor is owed — the QUESTION is void, not merely unasked.
func TestManagerLoadResidentDocCount(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// LoadResidentDocCount over an OSS Manager with a WARM L2 returns the L2 resident
	// count, matching ResidentDocCount after the load.
	dir := t.TempDir()
	prod := closeOnCleanup(t, NewManager(dir, 0))
	seedShipped(t, ctx, prod, kgtypes.GraphCode, "warm", hnswVecDocs(80))
	require.NoError(t, prod.Flush(ctx, kgtypes.GraphCode, "warm"))

	cons := closeOnCleanup(t, NewManager(dir, 0))
	got, err := cons.LoadResidentDocCount(ctx, kgtypes.GraphCode, "warm")
	require.NoError(t, err)
	require.Equal(t, 80, got, "LoadResidentDocCount loads L2 and returns the resident count")
	require.Equal(t, got, cons.ResidentDocCount(kgtypes.GraphCode, "warm"),
		"after the load, the raw ResidentDocCount matches")
}
