// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"fmt"
	"math/rand/v2"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// detStressDocs builds a deterministic n-doc corpus with distinct 32-byte vectors.
// Vectors are drawn from a fixed PCG seed so the corpus is identical run-to-run —
// the input side of the convergence proof must itself be stable.
func detStressDocs(n int) []searchengine.Document {
	rng := rand.New(rand.NewPCG(0xC0FFEE, 0x1CECAFE))
	docs := make([]searchengine.Document, n)
	for i := range docs {
		v := make([]byte, 32)
		for j := range v {
			v[j] = byte(rng.UintN(256))
		}
		docs[i] = searchengine.Document{ID: fmt.Sprintf("n%d", i), Vector: v}
	}
	return docs
}

// shipDeterministicAndExport builds the corpus through a Manager whose embed engine
// is pinned to the DETERMINISTIC serial builder and returns the single sealed HNSW
// blob (its content-hash ID + raw Encode bytes). This drives the real
// Manager write → engine seal → Format.Build → Encode path, not a bare builder.
func shipDeterministicAndExport(t *testing.T, docs []searchengine.Document) searchengine.SegmentBlob {
	t.Helper()

	// The embed ship path builds deterministically by default now — no seam to set.
	mgr := closeOnCleanup(t, NewManager(t.TempDir(), 0))

	require.NoError(t, mgr.AddAndMarkDirty(context.Background(), kgtypes.GraphKnowledge, "kg", docs))

	exported := mgr.managerFor(kgtypes.GraphKnowledge, "kg").engine.Export()
	require.Len(t, exported, 1, "1024 docs seal exactly one segment")
	return exported[0]
}

// TestDeterministicConvergenceAcrossWriters is the cross-WRITER convergence proof
// the content-addressed registry depends on: TWO INDEPENDENT Managers (separate
// harnesses, separate cache dirs — mimicking two daemons building the same graph)
// must produce a BYTE-IDENTICAL sealed HNSW segment, and therefore the SAME
// content-hash SegmentID. Without it the server holds one blob per writer; with it
// the registry dedups to a single copy at refcount-N. This is what the deterministic
// builder exists to deliver (and what the segment_rebuild path relies on).
func TestDeterministicConvergenceAcrossWriters(t *testing.T) {
	t.Parallel()

	docs := detStressDocs(1024)

	// Two fully independent Managers — distinct fake services, distinct L2 caches.
	blobA := shipDeterministicAndExport(t, docs)
	blobB := shipDeterministicAndExport(t, docs)

	require.Equal(t, blobA.Bytes, blobB.Bytes,
		"two independent writers must build a byte-identical deterministic segment")
	require.Equal(t, blobA.ID, blobB.ID,
		"byte-identical Encode ⇒ identical content-hash SegmentID (registry dedups to one copy)")
}

// TestDeterministicConvergenceRepeated rebuilds the same corpus through the
// deterministic embed path many times (>= 5) and asserts every sealed segment's
// content hash + bytes equal the first — flushing out any residual run-to-run
// non-determinism that a single A-vs-B comparison might miss.
func TestDeterministicConvergenceRepeated(t *testing.T) {
	t.Parallel()

	docs := detStressDocs(1024)

	first := shipDeterministicAndExport(t, docs)
	for i := range 5 {
		got := shipDeterministicAndExport(t, docs)
		require.Equal(t, first.ID, got.ID, "rebuild %d content hash diverged", i)
		require.Equal(t, first.Bytes, got.Bytes, "rebuild %d bytes diverged", i)
	}
}

// TestDeterministicExactTop1Recovery500 guards the exact-match recall the earlier
// concurrent builder missed ~0.6% of the time: over the deterministic serial build,
// every one of 500 indexed probes must recover its OWN node as the top-1 nearest
// neighbor (exact-match self-recall = 500/500). It drives the deterministic embed
// write + tick path and then searches the resident engine with each
// indexed vector.
func TestDeterministicExactTop1Recovery500(t *testing.T) {
	t.Parallel()

	const n = 500
	docs := detStressDocs(n)
	ctx := context.Background()

	mgr := closeOnCleanup(t, NewManager(t.TempDir(), 0))

	// n < MinSegmentDocs(1024). The write force-seals the batch and the tick ships
	// it; the trailing Flush is a no-op on the now-empty buffer and is kept as the
	// quiescence call the one-shot path makes.
	seedShipped(t, ctx, mgr, kgtypes.GraphKnowledge, "kg", docs)
	require.NoError(t, mgr.Flush(ctx, kgtypes.GraphKnowledge, "kg"))

	hit := 0
	for _, d := range docs {
		hits, err := mgr.Search(ctx, kgtypes.GraphKnowledge, "kg", "", d.Vector, 1)
		require.NoError(t, err)
		if len(hits) > 0 && hits[0].ID == d.ID {
			hit++
		}
	}
	require.Equal(t, n, hit,
		"deterministic serial build must recover the exact-match node as top-1 for all %d probes (got %d/%d)", n, hit, n)
}
