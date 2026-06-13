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

// searchCorpusN is the fixed corpus size every searchCorpus caller uses: ==
// MinSegmentDocs default, so AddAndShip / the deterministic chunker seals exactly
// one segment per format.
const searchCorpusN = 1024

// searchCorpus builds a searchCorpusN-doc corpus (== MinSegmentDocs, so AddAndShip
// seals exactly one segment per format) for BOTH formats. Every doc carries a
// vector AND BM25 fields. One designated "target" doc is the discriminator: it
// carries a UNIQUE BM25 term that no other doc has, and its vector is returned so
// the caller can use it as the query vector. Both arms therefore rank the target
// strongly: BM25 because the unique term has maximal IDF, HNSW because the query
// vector is the target's own (exact-match nearest neighbor).
func searchCorpus(targetIdx int) (docs []searchengine.Document, targetID string, targetVec []byte, uniqueTerm string) {
	const n = searchCorpusN
	rng := rand.New(rand.NewPCG(0x5EED, 0xF00D))
	docs = make([]searchengine.Document, n)
	uniqueTerm = "zzqqxxuniquetarget"
	for i := range docs {
		v := make([]byte, 32)
		for j := range v {
			v[j] = byte(rng.UintN(256))
		}
		id := fmt.Sprintf("n%d", i)
		summary := "shared corpus filler body common token"
		if i == targetIdx {
			summary = uniqueTerm + " " + summary
			targetID = id
			targetVec = v
		}
		docs[i] = searchengine.Document{
			ID:     id,
			Vector: v,
			Fields: map[string]string{
				searchengine.FieldSummary: summary,
			},
		}
	}
	return docs, targetID, targetVec, uniqueTerm
}

// TestManagerSearchFusesBothEngines is Phase 2 Step 1's criterion: Manager.Search
// loads both per-graph engines, searches HNSW with the query vector and BM25 with
// the query text, and returns RRF-fused ranked Hits. A doc that is strong in BOTH
// arms (unique BM25 term + exact-match query vector) is the fused #1.
//
// Run under -race (the criterion requires the concurrent two-engine fan-out be
// race-clean); `make test` / `go test -race ./...` exercise that.
func TestManagerSearchFusesBothEngines(t *testing.T) {
	_, gc := newSegmentHarness(t)
	ctx := context.Background()

	docs, targetID, targetVec, uniqueTerm := searchCorpus(7)

	mgr := NewManager(gc, t.TempDir(), 0)

	// Ship both formats for one graph.
	require.NoError(t, mgr.AddAndShip(ctx, kgtypes.GraphKnowledge, "kg", docs))
	require.NoError(t, mgr.AddAndShipFields(ctx, kgtypes.GraphKnowledge, "kg", docs))

	// Fused search: the target is strong in BOTH arms → fused #1.
	fused, err := mgr.Search(ctx, kgtypes.GraphKnowledge, "kg", uniqueTerm, targetVec, 10)
	require.NoError(t, err)
	require.NotEmpty(t, fused, "fused search returns hits")
	require.Equal(t, targetID, fused[0].ID, "doc strong in BOTH arms is the RRF #1")

	// Fused scores are descending (RRF post-condition).
	for i := 1; i < len(fused); i++ {
		require.LessOrEqual(t, fused[i].Score, fused[i-1].Score, "fused hits are sorted descending")
	}
}

// TestManagerSearchTextOnlyArm asserts a query with NO vector exercises only the
// BM25 arm (the HNSW arm is skipped on empty queryVec) and still returns the
// unique-term doc as the top fused hit. Proves the single-modality degrade path.
func TestManagerSearchTextOnlyArm(t *testing.T) {
	_, gc := newSegmentHarness(t)
	ctx := context.Background()

	docs, targetID, _, uniqueTerm := searchCorpus(3)

	mgr := NewManager(gc, t.TempDir(), 0)
	require.NoError(t, mgr.AddAndShip(ctx, kgtypes.GraphKnowledge, "kg", docs))
	require.NoError(t, mgr.AddAndShipFields(ctx, kgtypes.GraphKnowledge, "kg", docs))

	fused, err := mgr.Search(ctx, kgtypes.GraphKnowledge, "kg", uniqueTerm, nil, 10)
	require.NoError(t, err)
	require.NotEmpty(t, fused, "text-only fused search returns BM25 hits")
	require.Equal(t, targetID, fused[0].ID, "unique-term doc is BM25 (and fused) #1")
}

// TestManagerSearchEmptyGraph asserts searching a graph with no shipped segments
// returns an empty fused list (not an error) — the engine's empty-set return.
func TestManagerSearchEmptyGraph(t *testing.T) {
	_, gc := newSegmentHarness(t)
	ctx := context.Background()

	mgr := NewManager(gc, t.TempDir(), 0)
	fused, err := mgr.Search(ctx, kgtypes.GraphKnowledge, "never-shipped", "anything", make([]byte, 32), 10)
	require.NoError(t, err)
	require.Empty(t, fused, "search over an unbuilt graph returns empty, not error")
}
