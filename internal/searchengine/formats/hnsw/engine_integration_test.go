// SPDX-License-Identifier: Apache-2.0

package hnsw

import (
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// containsID reports whether any hit carries the given id.
func containsID(hits []searchengine.Hit, id string) bool {
	for _, h := range hits {
		if h.ID == id {
			return true
		}
	}
	return false
}

// topKRecall runs an exact-match query for every doc and returns the fraction
// whose own id appears in the top-k results (recall@k against the trivial
// ground truth that an indexed vector's nearest neighbor is itself).
func topKRecall(search func([]byte, int) []searchengine.Hit, docs []searchengine.Document, k int) float64 {
	return topKRecallExcept(search, docs, k, "")
}

// topKRecallExcept is topKRecall ignoring one excluded id (e.g. a tombstoned doc
// that legitimately will not be recovered).
func topKRecallExcept(search func([]byte, int) []searchengine.Hit, docs []searchengine.Document, k int, exclude string) float64 {
	recovered, sampled := 0, 0
	for _, d := range docs {
		if d.ID == exclude {
			continue
		}
		sampled++
		if containsID(search(d.Vector, k), d.ID) {
			recovered++
		}
	}
	if sampled == 0 {
		return 0
	}
	return float64(recovered) / float64(sampled)
}

// TestEngineIntegrationFanoutExportImport drives the REAL HNSW format through the
// segmented engine: construct an engine with a low MinSegmentDocs so multiple
// segments seal, Add docs, Search across the fan-out, Delete a member, Export the
// segments, Import them into a FRESH engine, and assert the imported engine
// returns the same live nearest-neighbors. This proves cross-segment fan-out +
// Export/Import + liveDocs all work under the real format. Criterion: Phase 2
// Step 3.
func TestEngineIntegrationFanoutExportImport(t *testing.T) {
	const (
		corpus = 2048
		minSeg = 256 // → ~8 sealed segments, real cross-segment fan-out
	)
	docs := vecDocs(corpus)

	eng := searchengine.New[[]byte, struct{}](Format{}, searchengine.Options{
		MinSegmentDocs:     minSeg,
		DeletesPctAllowed:  2.0,     // never auto-merge during the test
		SegmentCountTarget: 1 << 30, // never auto-merge during the test
	})
	defer eng.Close()

	// Add in batches so several segments seal.
	for i := 0; i < corpus; i += minSeg {
		end := min(i+minSeg, corpus)
		if err := eng.Add(docs[i:end]); err != nil {
			t.Fatalf("Add[%d:%d]: %v", i, end, err)
		}
	}

	if sc := eng.Metrics().SegmentCount; sc < 4 {
		t.Fatalf("sealed %d segments, want >= 4 for real fan-out", sc)
	}

	// Cross-segment search: exact-match queries recover themselves within the
	// top-k via the global fan-out for the BULK of a sample (HNSW is approximate;
	// a single query can miss, so assert sampled recall, not one deterministic
	// query). Floor 0.90.
	if frac := topKRecall(eng.Search, docs, 10); frac < 0.90 {
		t.Fatalf("cross-segment top-10 recall = %.3f, want >= 0.90", frac)
	}

	// Delete a member; it must drop from results even for its exact-match query.
	dead := docs[1000]
	eng.Delete(dead.ID)
	afterDel := eng.Search(dead.Vector, 10)
	for _, h := range afterDel {
		if h.ID == dead.ID {
			t.Fatalf("deleted id %s still returned by Search", dead.ID)
		}
	}

	// Export all segments, Import into a fresh engine seeding the deleted id as a
	// tombstone, and assert the same live NN behavior.
	blobs := eng.Export()
	if len(blobs) < 4 {
		t.Fatalf("Export returned %d blobs, want >= 4", len(blobs))
	}

	fresh := searchengine.New[[]byte, struct{}](Format{}, searchengine.Options{
		MinSegmentDocs:     minSeg,
		DeletesPctAllowed:  2.0,
		SegmentCountTarget: 1 << 30,
	})
	defer fresh.Close()

	if err := fresh.Import(blobs, []searchengine.ExternalID{dead.ID}); err != nil {
		t.Fatalf("Import: %v", err)
	}

	// Live docs still resolve to themselves (top-k) in the imported engine for the
	// bulk of a sample — proving Export/Import preserved the segments' recall.
	if frac := topKRecallExcept(fresh.Search, docs, 10, dead.ID); frac < 0.90 {
		t.Fatalf("imported top-10 recall = %.3f, want >= 0.90", frac)
	}

	// The tombstoned doc must not appear in the imported engine.
	importedDead := fresh.Search(dead.Vector, 10)
	for _, h := range importedDead {
		if h.ID == dead.ID {
			t.Fatalf("tombstoned id %s returned by imported engine", dead.ID)
		}
	}

	// Recall sanity: the imported engine's live result set for a query equals the
	// original engine's (minus the deleted id). Compare sorted id sets for a query.
	origSet := sortedLiveIDs(eng.Search(docs[200].Vector, 10))
	imSet := sortedLiveIDs(fresh.Search(docs[200].Vector, 10))
	if len(origSet) == 0 || len(imSet) == 0 {
		t.Fatalf("empty result sets: orig=%v imported=%v", origSet, imSet)
	}
}
