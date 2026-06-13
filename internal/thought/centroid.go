// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"encoding/binary"
	"math/bits"
)

// Binary vectors in the thought layer are 256-bit (32-byte) packed bit
// strings, matching the embedder's EmbedBinary output width.
const (
	vectorBits  = 256
	vectorBytes = 32
)

// BitMajorityCentroid computes the per-bit majority over a set of 256-bit
// (32-byte) member vectors. Bit i of the result is set iff a STRICT majority
// of the valid members set bit i; an exact tie (half the members set it)
// clears the bit. Members whose length is not vectorBytes (including the
// zero-length case) are skipped defensively and do not count toward the
// majority denominator, so a malformed vector cannot corrupt the tally or
// panic. Returns nil when no valid members are present.
func BitMajorityCentroid(vectors [][]byte) []byte {
	// Per-bit count of members that set the bit.
	counts := make([]int, vectorBits)
	valid := 0
	for _, v := range vectors {
		if len(v) != vectorBytes {
			continue
		}
		valid++
		for byteIdx, b := range v {
			if b == 0 {
				continue
			}
			base := byteIdx * 8
			for bit := range 8 {
				if b&(1<<uint(bit)) != 0 {
					counts[base+bit]++
				}
			}
		}
	}
	if valid == 0 {
		return nil
	}
	out := make([]byte, vectorBytes)
	for i := range vectorBits {
		// Strict majority: 2*count > valid sets the bit; an exact tie
		// (2*count == valid) leaves it cleared.
		if 2*counts[i] > valid {
			out[i/8] |= 1 << uint(i%8)
		}
	}
	return out
}

// ComputeClusterCentroids is a LEVER-TIME helper (Phase 4 only — NOT the hourly
// cluster-detection pass) that, for each cluster, sets cluster.Centroid to the
// bit-majority of its member vectors and cluster.MedoidID to the member node ID
// whose vector is bit-closest to that centroid. It operates purely over the
// in-memory clusters and the already-drained vector index — no wire calls.
//
// When a cluster has no member vectors in the index (degraded client with no
// scanner, or a cold graph), its Centroid stays nil and MedoidID stays empty;
// the caller reports a degraded run. A nil/empty index leaves every cluster's
// Centroid/MedoidID untouched. Clusters is mutated in place.
func ComputeClusterCentroids(clusters []ThoughtCluster, vectorIndex map[string][]byte) {
	for i := range clusters {
		c := &clusters[i]
		memberVecs := make([][]byte, 0, len(c.ThoughtIDs))
		for _, id := range c.ThoughtIDs {
			if v, ok := vectorIndex[id]; ok {
				memberVecs = append(memberVecs, v)
			}
		}
		centroid := BitMajorityCentroid(memberVecs)
		if centroid == nil {
			// No valid member vectors — leave Centroid/MedoidID zero.
			continue
		}
		c.Centroid = centroid
		c.MedoidID = medoidOf(c.ThoughtIDs, vectorIndex, centroid)
	}
}

// medoidOf returns the member node ID whose vector is bit-closest (max
// BitSimilarity) to the centroid. Ties break to the lowest member ID encountered
// in the cluster's ThoughtIDs order, giving a deterministic anchor. Returns ""
// when no member has a valid vector in the index.
func medoidOf(memberIDs []string, vectorIndex map[string][]byte, centroid []byte) string {
	bestID := ""
	bestSim := -1.0
	for _, id := range memberIDs {
		v, ok := vectorIndex[id]
		if !ok {
			continue
		}
		sim := BitSimilarity(v, centroid)
		if sim > bestSim {
			bestSim = sim
			bestID = id
		}
	}
	return bestID
}

// BitSimilarity returns the bit-agreement fraction between two 256-bit
// (32-byte) vectors: 1 - hammingDistance(a,b)/256, a value in [0,1] where
// 1.0 means identical. Vectors that are not both vectorBytes long yield 0
// (maximally dissimilar) rather than panicking. The popcount arithmetic
// mirrors the hnsw distance reducer without depending on its private symbol.
func BitSimilarity(a, b []byte) float64 {
	if len(a) != vectorBytes || len(b) != vectorBytes {
		return 0
	}
	dist := 0
	i := 0
	for ; i+8 <= vectorBytes; i += 8 {
		x := binary.LittleEndian.Uint64(a[i:]) ^ binary.LittleEndian.Uint64(b[i:])
		dist += bits.OnesCount64(x)
	}
	for ; i < vectorBytes; i++ {
		dist += bits.OnesCount8(a[i] ^ b[i])
	}
	return 1 - float64(dist)/float64(vectorBits)
}
