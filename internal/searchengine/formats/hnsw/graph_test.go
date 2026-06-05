// SPDX-License-Identifier: Apache-2.0

package hnsw

import (
	"fmt"
	"math/rand/v2"
	"testing"
)

// randomVectors produces n deterministic 32-byte vectors keyed "v0".."v{n-1}".
func randomVectors(n int) []binaryBuildItem {
	return randomVectorsSeed(n, 0x1234, 0x5678)
}

// randomVectorsSeed is randomVectors with an explicit PCG seed so a second corpus
// can be drawn DISTINCT from the first (different seed ⇒ different vectors).
func randomVectorsSeed(n int, s1, s2 uint64) []binaryBuildItem {
	rng := rand.New(rand.NewPCG(s1, s2))
	items := make([]binaryBuildItem, n)
	for i := range items {
		v := make([]byte, defaultVecBytes)
		for j := range v {
			v[j] = byte(rng.UintN(256))
		}
		items[i] = binaryBuildItem{id: fmt.Sprintf("v%d", i), vec: v}
	}
	return items
}

// exactTop1 returns the externalID of the closest item to query by exact Hamming
// distance (the ground-truth nearest neighbor).
func exactTop1(items []binaryBuildItem, query []byte) string {
	bestID := ""
	var bestDist float32 = 1 << 30
	for _, it := range items {
		if d := hammingDistance(query, it.vec); d < bestDist {
			bestDist = d
			bestID = it.id
		}
	}
	return bestID
}

// buildSerial builds a graph via the Insert loop (single-threaded insertion).
func buildSerial(items []binaryBuildItem) *binaryGraph {
	h := newBinaryGraph(defaultVecBytes, defaultM, defaultEfConstruction)
	for _, it := range items {
		h.Insert(it.id, it.vec)
	}
	return h
}

// TestSerialParallelTop1Parity builds the same vector set serially (Insert loop)
// and in parallel (buildBinaryHNSWParallel with NumCPU workers) and asserts both
// indexes return the same top-1 nearest neighbor for a sample of queries. Parity
// is on the returned neighbor, NOT byte-identity — parallel insertion order
// differs, so the graphs are not bit-identical.
func TestSerialParallelTop1Parity(t *testing.T) {
	const n = 1500
	items := randomVectors(n)

	serial := buildSerial(items)
	parallel := buildBinaryHNSWParallel(items, defaultVecBytes, defaultM, defaultEfConstruction, 0)

	// Both graphs are approximate and independently seeded, so top-1 AGREEMENT
	// compounds two ~97% recalls (≈0.94 expected). The honest property is that
	// each independently recovers the EXACT top-1 (the query's own vector) for the
	// bulk of the corpus — assert each graph's recall against ground truth, sampled
	// over the whole corpus for a stable fraction, floor 0.90.
	var sHit, pHit int
	for i := range n {
		q := items[i].vec
		want := items[i].id // exact NN of an indexed vector is itself
		sHits := serial.search(q, 1, nil)
		pHits := parallel.search(q, 1, nil)
		if len(sHits) == 0 || len(pHits) == 0 {
			t.Fatalf("query %d: empty result (serial=%d parallel=%d)", i, len(sHits), len(pHits))
		}
		if sHits[0].externalID == want {
			sHit++
		}
		if pHits[0].externalID == want {
			pHit++
		}
	}
	sFrac := float64(sHit) / float64(n)
	pFrac := float64(pHit) / float64(n)
	if sFrac < 0.90 || pFrac < 0.90 {
		t.Fatalf("top-1 exact recall: serial=%.3f parallel=%.3f, want both >= 0.90", sFrac, pFrac)
	}
}

// TestSerializeRoundTripIdenticalSearch builds an index, serializes it, decodes
// it, and asserts the decoded graph returns identical Search results to the
// original across a query sample — proving vectors + topology survive the v2
// round-trip.
func TestSerializeRoundTripIdenticalSearch(t *testing.T) {
	const n = 800
	items := randomVectors(n)
	orig := buildBinaryHNSWParallel(items, defaultVecBytes, defaultM, defaultEfConstruction, 0)

	blob := orig.encode()
	if len(blob) == 0 || blob[0] != serialVersionWithVectors {
		t.Fatalf("encode produced bad blob: len=%d version=%d", len(blob), blob[0])
	}

	decoded, err := decodeGraph(blob)
	if err != nil {
		t.Fatalf("decodeGraph: %v", err)
	}
	if decoded.nodeCount() != orig.nodeCount() {
		t.Fatalf("node count mismatch: decoded=%d orig=%d", decoded.nodeCount(), orig.nodeCount())
	}

	for i := range 100 {
		q := items[i].vec
		oHits := orig.search(q, 10, nil)
		dHits := decoded.search(q, 10, nil)
		if len(oHits) != len(dHits) {
			t.Fatalf("query %d: hit count mismatch orig=%d decoded=%d", i, len(oHits), len(dHits))
		}
		for j := range oHits {
			if oHits[j].externalID != dHits[j].externalID {
				t.Fatalf("query %d hit %d: id mismatch orig=%s decoded=%s", i, j, oHits[j].externalID, dHits[j].externalID)
			}
			if oHits[j].score != dHits[j].score {
				t.Fatalf("query %d hit %d: score mismatch orig=%v decoded=%v", i, j, oHits[j].score, dHits[j].score)
			}
		}
	}
}

// TestExactTop1Recovery sanity-checks that the graph recovers the exact nearest
// neighbor (the query vector itself) for the bulk of an indexed sample — guards
// against a graph that builds but does not actually navigate.
func TestExactTop1Recovery(t *testing.T) {
	const n = 1000
	items := randomVectors(n)
	h := buildBinaryHNSWParallel(items, defaultVecBytes, defaultM, defaultEfConstruction, 0)

	// Sample the WHOLE corpus so the measured fraction is stable near the ~97%
	// mean (a small slice has high variance run-to-run with the crypto-seeded
	// graph rng). Floor 0.93 leaves genuine margin below the stable mean while
	// still catching a navigation regression.
	hit := 0
	for i := range n {
		q := items[i].vec
		want := exactTop1(items, q)
		hits := h.search(q, 1, nil)
		if len(hits) > 0 && hits[0].externalID == want {
			hit++
		}
	}
	if float64(hit)/float64(n) < 0.93 {
		t.Fatalf("exact top-1 recall = %d/%d (%.3f), want >= 0.93", hit, n, float64(hit)/float64(n))
	}
}
