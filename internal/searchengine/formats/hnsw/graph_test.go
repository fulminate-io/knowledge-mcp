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

// TestSerialBuildersTop1Recall builds the same vector set two ways — the
// crypto-seeded Insert-loop helper (buildSerial) and the production fixed-seed
// deterministic builder (buildBinaryHNSWSerialDeterministic) — and asserts each
// independently recovers the EXACT top-1 (the query's own indexed vector) for the
// bulk of the corpus. Both are serial; the seed/insertion-order difference is the
// only delta, so this guards that the deterministic builder navigates as well as a
// plain serial build (recall floor 0.90), not just that it builds.
func TestSerialBuildersTop1Recall(t *testing.T) {
	const n = 1500
	items := randomVectors(n)

	serial := buildSerial(items)
	deterministic := buildBinaryHNSWSerialDeterministic(items, defaultVecBytes, dtypeUbinary, defaultM, defaultEfConstruction)

	// Both graphs are approximate, so top-1 AGREEMENT compounds two ~97% recalls.
	// The honest property is that each independently recovers the EXACT top-1 (the
	// query's own vector) for the bulk of the corpus — assert each graph's recall
	// against ground truth, sampled over the whole corpus, floor 0.90.
	var sHit, dHit int
	for i := range n {
		q := items[i].vec
		want := items[i].id // exact NN of an indexed vector is itself
		sHits := serial.search(q, 1, nil)
		dHits := deterministic.search(q, 1, nil)
		if len(sHits) == 0 || len(dHits) == 0 {
			t.Fatalf("query %d: empty result (serial=%d deterministic=%d)", i, len(sHits), len(dHits))
		}
		if sHits[0].externalID == want {
			sHit++
		}
		if dHits[0].externalID == want {
			dHit++
		}
	}
	sFrac := float64(sHit) / float64(n)
	dFrac := float64(dHit) / float64(n)
	if sFrac < 0.90 || dFrac < 0.90 {
		t.Fatalf("top-1 exact recall: serial=%.3f deterministic=%.3f, want both >= 0.90", sFrac, dFrac)
	}
}

// TestSerializeRoundTripIdenticalSearch builds an index, serializes it, decodes
// it, and asserts the decoded graph returns identical Search results to the
// original across a query sample — proving vectors + topology survive the v2
// round-trip.
func TestSerializeRoundTripIdenticalSearch(t *testing.T) {
	const n = 800
	items := randomVectors(n)
	orig := buildBinaryHNSWSerialDeterministic(items, defaultVecBytes, dtypeUbinary, defaultM, defaultEfConstruction)

	blob, err := encodeGraphV3(orig)
	if err != nil {
		t.Fatalf("encodeGraphV3: %v", err)
	}
	if len(blob) == 0 || blob[0] != serialVersionOffsets {
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
	h := buildBinaryHNSWSerialDeterministic(items, defaultVecBytes, dtypeUbinary, defaultM, defaultEfConstruction)

	// Sample the WHOLE corpus so the measured fraction is a stable estimate of the
	// ~97% mean (the build is deterministic, but per-node recall varies across the
	// corpus, so a small slice is a noisy sample). Floor 0.93 leaves genuine margin
	// below the stable mean while still catching a navigation regression.
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
