// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// TestManagerVectorByIDResolvesShippedVector is Phase 2's criterion: after a known
// corpus is shipped for one graph, Manager.VectorByID(ctx, gt, name, knownID)
// returns (storedVec, true, nil) byte-equal to the shipped vector, and an unknown
// id returns (nil, false, nil). The resolve runs on a FRESH Manager that never
// searched — proving dm.load(ctx) pulled the graph's segments cache-first before
// the lookup, exactly as Manager.Search does, and that the (ok,err) tuple separates
// absent-id (ok=false, err=nil) from a load failure. Fails-when-absent: skipping
// dm.load(ctx) returns (nil,false) for a known id on a fresh manager.
func TestManagerVectorByIDResolvesShippedVector(t *testing.T) {
	t.Parallel()

	_, gc := newSegmentHarness(t)
	ctx := context.Background()

	docs, targetID, targetVec, _ := searchCorpus(11)

	// Ship the HNSW segment with one manager.
	shipper := NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0, withSegmentSource(gc))
	seedShipped(t, ctx, shipper, kgtypes.GraphKnowledge, "kg", docs)

	// Resolve on a FRESH manager that has never searched — it must pull the shipped
	// segments cache-first via dm.load(ctx) before the by-id read.
	fresh := NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0, withSegmentSource(gc))

	vec, ok, err := fresh.VectorByID(ctx, kgtypes.GraphKnowledge, "kg", targetID)
	require.NoError(t, err)
	require.True(t, ok, "a shipped node's vector resolves on a fresh manager (cache-first load ran)")
	require.True(t, bytes.Equal(vec, targetVec), "resolved vector is byte-equal to the shipped vector")

	// Unknown id: loaded fine, but no such member → (nil, false, nil), NOT an error.
	got, ok, err := fresh.VectorByID(ctx, kgtypes.GraphKnowledge, "kg", "no-such-node")
	require.NoError(t, err, "an absent id is not a load error")
	require.False(t, ok, "an absent id resolves ok=false")
	require.Nil(t, got, "an absent id resolves a nil vector")
}

// TestManagerVectorByIDEmptyGraph asserts VectorByID over a graph with no shipped
// segments returns (nil, false, nil) — a clean load of an empty delta, never an
// error and never a silent wrong vector. The caller turns ok=false into the loud
// guidance error.
func TestManagerVectorByIDEmptyGraph(t *testing.T) {
	t.Parallel()

	_, gc := newSegmentHarness(t)
	ctx := context.Background()

	mgr := NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0, withSegmentSource(gc))
	got, ok, err := mgr.VectorByID(ctx, kgtypes.GraphKnowledge, "never-shipped", "anything")
	require.NoError(t, err)
	require.False(t, ok)
	require.Nil(t, got)
}
