// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// TestDeterministicShipKeepsBothFormatsResolvable models the REAL post-rebuild
// state: manage(rebuild_segments) Adds BOTH the deterministic HNSW segments and
// the BM25 segments for one graph, then FlushDeterministic ships them in one
// finalize. The server keys blobs by graphKey ONLY (no format dimension), so each
// engine's List(0) returns BOTH formats' blobs.
//
// REGRESSION: without the keepFormat filter in ensureShippedSeeded, the
// BM25 ship's reconcilePrune treats the just-shipped HNSW segment as "shipped but
// no longer Exported" (the BM25 engine never Exports an HNSW blob) and PRUNES it
// server-side. The HNSW segments vanish, so a fresh manager's VectorByID resolves
// NOTHING (while BM25-arm text search still 'works' — masking the data loss). This
// test fails-when-absent: with the bug, the require.True(ok) below fails because
// the HNSW segment was pruned.
func TestDeterministicShipKeepsBothFormatsResolvable(t *testing.T) {
	svc, gc := newSegmentHarness(t)
	ctx := context.Background()

	// searchCorpusN == MinSegmentDocs → exactly one full deterministic chunk per format.
	docs, targetID, targetVec, _ := searchCorpus(11)

	// Ship via the DETERMINISTIC rebuild path: Add BOTH formats, then the single
	// serial FlushDeterministic finalize — exactly what RebuildSegments drives.
	shipper := NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0, withSegmentSource(gc))
	require.NoError(t, shipper.AddDeterministic(ctx, kgtypes.GraphKnowledge, "kg", docs))
	require.NoError(t, shipper.AddFields(ctx, kgtypes.GraphKnowledge, "kg", docs))
	_, err := shipper.FlushDeterministic(ctx, kgtypes.GraphKnowledge, "kg")
	require.NoError(t, err)

	// BOTH formats must survive on the server — the BM25 ship must NOT prune the
	// HNSW segment (and vice versa).
	hnsw, bm25 := countShippedByFormat(svc)
	require.Positive(t, hnsw, "the HNSW segment must survive the BM25 ship's reconcilePrune")
	require.Positive(t, bm25, "the BM25 segment must survive too")

	// A FRESH read manager resolves the target's STORED vector by id — the exact
	// read mode:'similar' performs. This is the user-visible failure under the bug.
	fresh := NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0, withSegmentSource(gc))
	vec, ok, err := fresh.VectorByID(ctx, kgtypes.GraphKnowledge, "kg", targetID)
	require.NoError(t, err)
	require.True(t, ok, "VectorByID must resolve the deterministic-shipped vector after a both-formats rebuild ship")
	require.True(t, bytes.Equal(vec, targetVec), "resolved vector byte-equal to the shipped vector")
}

// TestEmbedShipKeepsBothFormatsResolvable is the embed-path twin of the
// deterministic test: AddAndShip (HNSW) + AddAndShipFields (BM25) interleave the
// per-format ships, which is the same cross-format prune exposure. Both formats
// must survive and the by-id read must resolve.
func TestEmbedShipKeepsBothFormatsResolvable(t *testing.T) {
	svc, gc := newSegmentHarness(t)
	ctx := context.Background()

	docs, targetID, targetVec, _ := searchCorpus(7)

	mgr := NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0, withSegmentSource(gc))
	require.NoError(t, mgr.AddAndShip(ctx, kgtypes.GraphKnowledge, "kg", docs))
	require.NoError(t, mgr.AddAndShipFields(ctx, kgtypes.GraphKnowledge, "kg", docs))

	hnsw, bm25 := countShippedByFormat(svc)
	require.Positive(t, hnsw, "embed HNSW segment must survive the BM25 ship's reconcilePrune")
	require.Positive(t, bm25, "embed BM25 segment must survive")

	fresh := NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0, withSegmentSource(gc))
	vec, ok, err := fresh.VectorByID(ctx, kgtypes.GraphKnowledge, "kg", targetID)
	require.NoError(t, err)
	require.True(t, ok, "VectorByID resolves the embed-shipped vector when both formats coexist on one graph key")
	require.True(t, bytes.Equal(vec, targetVec), "byte-equal")
}

// countShippedByFormat tallies the fake server's stored blobs by Format across
// every graph key.
func countShippedByFormat(svc *sharedServerFake) (hnsw, bm25 int) {
	svc.mu.Lock()
	defer svc.mu.Unlock()
	for _, blobs := range svc.byKey {
		for _, b := range blobs {
			if b.GetFormat() == "hnsw" {
				hnsw++
			} else {
				bm25++
			}
		}
	}
	return hnsw, bm25
}
