// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// multiwriter_convergence_test.go proves the registry's convergence property is a
// REAL reference count, not a ship-idempotency artifact. The discriminating signal
// is the per-writer manifest WINDOW (blobRefCount / writerManifest, Phase 1), read
// directly — a "deduped server count" assertion would pass on content-hash
// idempotency alone even with a broken refcount, so this suite never rests on it.
// The negative arm (drop X, survive via the other writer, reap only when both drop)
// is the clause a no-op refcount cannot satisfy.

// convergeDocs builds a TINY deterministic n-doc corpus with a content-addressable
// id offset. Tiny (n well under residentBackstopFloor=64) so the publish coverage
// RATIO gate disarms — letting a writer legitimately publish a small distinct
// corpus when it drops the shared blob, without tripping the corpus-wipe guard. The
// vectors are seed-derived so the SAME (n, seed) is byte-identical across writers
// (the deterministic builder ⇒ one content-hash) and DISTINCT seeds never collide.
func convergeDocs(n, seed int) []searchengine.Document {
	docs := make([]searchengine.Document, n)
	for i := range docs {
		idx := seed*1000 + i
		vec := make([]byte, 32)
		for b := range vec {
			vec[b] = byte((idx*37 + b*11 + seed*101) % 251)
		}
		docs[i] = searchengine.Document{
			ID:     fmt.Sprintf("cv-s%d-%05d", seed, idx),
			Vector: vec,
		}
	}
	return docs
}

// addShipFlush AddAndShips then Flushes a sub-1024 corpus through mgr so the tail
// seals + publishes as mgr's manifest for (gt, name). Returns the single sealed
// HNSW blob id (the content-hash the registry keys on).
//
//nolint:unparam // gt kept on the signature — it pairs with name to form the graph selector this helper publishes to.
func addShipFlush(t *testing.T, mgr *Manager, gt kgtypes.GraphType, name string, docs []searchengine.Document) string {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, mgr.AddAndMarkDirty(ctx, gt, name, docs))
	require.NoError(t, mgr.Flush(ctx, gt, name))
	export := mgr.managerFor(gt, name).engine.Export()
	require.Len(t, export, 1, "the tiny corpus seals exactly one segment")
	return export[0].ID
}

// TestMultiWriterConvergenceRefcount is Case A: two writers building the SAME
// deterministic corpus converge on ONE content-hash blob referenced by BOTH
// manifests (refcount 2), proven via the manifest window — then the NEGATIVE ARM:
// writer0 drops X (survives via writer1), writer1 drops X (now reaped). A broken or
// no-op refcount fails either the survival or the final reap.
func TestMultiWriterConvergenceRefcount(t *testing.T) {
	t.Parallel()

	mgrs, svc := newMultiWriterFleet(t, 2)
	w0, w1 := mgrs[0], mgrs[1]
	gt, name := kgtypes.GraphKnowledge, "converge"
	target := graphSelector(gt, name)

	shared := convergeDocs(16, 0) // tiny ⇒ coverage ratio disarms; deterministic ⇒ shared X

	// Both writers build the SAME corpus ⇒ the SAME content-hash X.
	x0 := addShipFlush(t, w0.Manager, gt, name, shared)
	x1 := addShipFlush(t, w1.Manager, gt, name, shared)
	require.Equal(t, x0, x1,
		"two writers building the same deterministic corpus mint the SAME content-hash id (a determinism regression fails here)")
	x := x0

	// DISCRIMINATING: X is referenced by TWO distinct writer manifests (refcount 2) —
	// a property a deduped server count CANNOT prove.
	require.Equal(t, 2, blobRefCount(svc, target, x),
		"shared id X is referenced by both writers' manifests (refcount 2), not just deduped once")
	require.Contains(t, writerManifest(svc, target, w0.writerID, "hnsw"), x, "writer0's manifest references X")
	require.Contains(t, writerManifest(svc, target, w1.writerID, "hnsw"), x, "writer1's manifest references X")
	require.True(t, serverHasBlob(svc, target, x), "X present on the server while referenced")

	// NEGATIVE ARM part 1 — writer0 DROPS X. A fresh Manager with the SAME writer_id
	// over the SAME graph builds a DIFFERENT corpus and publishes, REPLACING writer0's
	// manifest with one that omits X (the restart/republish shape: manifest is keyed
	// by writer_id, swapped on publish).
	w0b := restartFleetMember(t, svc, 0, t.TempDir())
	y := addShipFlush(t, w0b.Manager, gt, name, convergeDocs(16, 1))
	require.NotEqual(t, x, y, "writer0's replacement corpus mints a DIFFERENT id")

	require.NotContains(t, writerManifest(svc, target, w0.writerID, "hnsw"), x,
		"writer0's manifest no longer references X after the republish")
	require.Equal(t, 1, blobRefCount(svc, target, x),
		"X refcount drops to 1 — still referenced by writer1")
	require.True(t, serverHasBlob(svc, target, x),
		"X SURVIVES writer0 dropping it — writer1 still references it (a refcount that reaps here is broken)")

	// NEGATIVE ARM part 2 — writer1 ALSO drops X ⇒ no manifest references it ⇒ reaped.
	w1b := restartFleetMember(t, svc, 1, t.TempDir())
	z := addShipFlush(t, w1b.Manager, gt, name, convergeDocs(16, 2))
	require.NotEqual(t, x, z)

	require.Equal(t, 0, blobRefCount(svc, target, x),
		"X refcount is 0 once BOTH writers drop it")
	require.False(t, serverHasBlob(svc, target, x),
		"X is reaped ONLY when no manifest references it (a refcount that never reaps is broken)")
	require.NotContains(t, shippedHNSWIDs(svc), x, "X is absent from the server after the final drop")
}

// TestMultiWriterSafeCoexistence is Case B: two writers minting DIFFERENT blob ids
// coexist — both copies survive both publishes and neither writer's publish-driven
// refcount-GC reaps the other's referenced blob (cross-writer reap == 0).
func TestMultiWriterSafeCoexistence(t *testing.T) {
	t.Parallel()

	mgrs, svc := newMultiWriterFleet(t, 2)
	w0, w1 := mgrs[0], mgrs[1]
	gt, name := kgtypes.GraphKnowledge, "coexist"
	target := graphSelector(gt, name)

	// Distinct corpora ⇒ distinct content-hash ids.
	a := addShipFlush(t, w0.Manager, gt, name, convergeDocs(16, 10))
	b := addShipFlush(t, w1.Manager, gt, name, convergeDocs(16, 20))
	require.NotEqual(t, a, b, "distinct corpora mint distinct ids")

	// Both survive after both publishes; neither reaped the other's blob.
	require.True(t, serverHasBlob(svc, target, a), "writer0's blob survives writer1's publish")
	require.True(t, serverHasBlob(svc, target, b), "writer1's blob survives writer0's publish")
	require.Equal(t, 1, blobRefCount(svc, target, a), "a referenced only by writer0")
	require.Equal(t, 1, blobRefCount(svc, target, b), "b referenced only by writer1")

	// Each writer's manifest references ONLY its own blob — no cross-writer reap.
	require.ElementsMatch(t, []string{a}, writerManifest(svc, target, w0.writerID, "hnsw"))
	require.ElementsMatch(t, []string{b}, writerManifest(svc, target, w1.writerID, "hnsw"))
	require.Equal(t, 2, serverSegCount(t, svc, target), "both distinct blobs coexist on the server")
}
