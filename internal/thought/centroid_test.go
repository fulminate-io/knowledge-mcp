// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"bytes"
	"testing"
)

// vec32 builds a 32-byte vector with every byte set to b.
func vec32(b byte) []byte {
	v := make([]byte, vectorBytes)
	for i := range v {
		v[i] = b
	}
	return v
}

// bitVec builds a 32-byte vector with exactly the named bit indices set.
func bitVec(setBits ...int) []byte {
	v := make([]byte, vectorBytes)
	for _, i := range setBits {
		v[i/8] |= 1 << uint(i%8)
	}
	return v
}

// TestBitMajorityCentroid_IdenticalMembers asserts that an all-identical
// member set yields exactly that vector.
func TestBitMajorityCentroid_IdenticalMembers(t *testing.T) {
	member := bitVec(0, 7, 8, 64, 255)
	got := BitMajorityCentroid([][]byte{member, member, member})
	if !bytes.Equal(got, member) {
		t.Fatalf("BitMajorityCentroid(identical) = %x, want %x", got, member)
	}
}

// TestBitMajorityCentroid_StrictMajority asserts the per-bit strict-majority
// rule: a bit set by >half the members is set; a bit set by <half is cleared.
func TestBitMajorityCentroid_StrictMajority(t *testing.T) {
	// Three members. Bit 0 set by all 3 (3/3 → set). Bit 1 set by 2/3 → set.
	// Bit 2 set by 1/3 → cleared. Bit 3 set by 0/3 → cleared.
	m1 := bitVec(0, 1, 2)
	m2 := bitVec(0, 1)
	m3 := bitVec(0)
	got := BitMajorityCentroid([][]byte{m1, m2, m3})
	want := bitVec(0, 1)
	if !bytes.Equal(got, want) {
		t.Fatalf("BitMajorityCentroid(majority) = %x, want %x", got, want)
	}
}

// TestBitMajorityCentroid_TieCleared asserts an exact even split clears the
// bit — the load-bearing tie→0 semantic the merge cascade relies on (a 1+1
// union zeroes every disagreement bit).
func TestBitMajorityCentroid_TieCleared(t *testing.T) {
	// Two members. Bit 0 set by both → set. Bit 1 set by one of two → TIE →
	// cleared. Bit 5 set by neither → cleared.
	m1 := bitVec(0, 1)
	m2 := bitVec(0)
	got := BitMajorityCentroid([][]byte{m1, m2})
	want := bitVec(0)
	if !bytes.Equal(got, want) {
		t.Fatalf("BitMajorityCentroid(tie) = %x, want %x (bit 1 must clear on a tie)", got, want)
	}
}

// TestBitMajorityCentroid_SkipsBadLength asserts wrong-length / zero-length
// member vectors are skipped without panicking and do not corrupt the tally
// or shift the majority denominator.
func TestBitMajorityCentroid_SkipsBadLength(t *testing.T) {
	good1 := bitVec(0, 1)
	good2 := bitVec(0, 1)
	// A short vector, a long vector, an empty vector, and a nil — all skipped.
	bad := [][]byte{good1, {0x01}, make([]byte, 16), good2, make([]byte, 64), {}, nil}
	got := BitMajorityCentroid(bad)
	// Only good1+good2 count: both set bits 0 and 1 → both set (2/2 majority).
	want := bitVec(0, 1)
	if !bytes.Equal(got, want) {
		t.Fatalf("BitMajorityCentroid(mixed-length) = %x, want %x", got, want)
	}
}

// TestBitMajorityCentroid_NoValidMembers asserts an all-invalid input returns
// nil rather than a zero vector or a panic.
func TestBitMajorityCentroid_NoValidMembers(t *testing.T) {
	if got := BitMajorityCentroid([][]byte{{0x01}, nil, {}}); got != nil {
		t.Fatalf("BitMajorityCentroid(no valid) = %x, want nil", got)
	}
	if got := BitMajorityCentroid(nil); got != nil {
		t.Fatalf("BitMajorityCentroid(nil) = %x, want nil", got)
	}
}

// TestComputeClusterCentroids asserts a seeded vector index sets each cluster's
// Centroid to the bit-majority of its members and MedoidID to the bit-closest
// member; a nil/empty index leaves Centroid/MedoidID zero with no panic.
func TestComputeClusterCentroids(t *testing.T) {
	// Cluster "x": members m1,m2,m3. m1=m2=bits{0,1}, m3=bits{0}. Majority over
	// 3 → bit 0 (3/3) and bit 1 (2/3) set → centroid = bits{0,1}. The medoid is
	// the member bit-closest to that centroid: m1 and m2 ARE the centroid
	// (sim 1.0), m3 differs by one bit. Tie between m1 and m2 → lowest in order
	// = m1.
	idx := map[string][]byte{
		"m1": bitVec(0, 1),
		"m2": bitVec(0, 1),
		"m3": bitVec(0),
		// "m4" deliberately absent from the index (no vector) — skipped.
	}
	clusters := []ThoughtCluster{
		{ID: "x", ThoughtIDs: []string{"m1", "m2", "m3", "m4"}},
	}
	ComputeClusterCentroids(clusters, idx)

	wantCentroid := bitVec(0, 1)
	if !bytes.Equal(clusters[0].Centroid, wantCentroid) {
		t.Fatalf("Centroid = %x, want %x", clusters[0].Centroid, wantCentroid)
	}
	if clusters[0].MedoidID != "m1" {
		t.Fatalf("MedoidID = %q, want m1 (bit-closest, tie→first-in-order)", clusters[0].MedoidID)
	}
}

// TestComputeClusterCentroids_NilIndex asserts a nil/empty index leaves the
// cluster's Centroid/MedoidID untouched and does not panic.
func TestComputeClusterCentroids_NilIndex(t *testing.T) {
	clusters := []ThoughtCluster{{ID: "x", ThoughtIDs: []string{"a", "b"}}}
	ComputeClusterCentroids(clusters, nil)
	if clusters[0].Centroid != nil {
		t.Fatalf("Centroid = %x, want nil for nil index", clusters[0].Centroid)
	}
	if clusters[0].MedoidID != "" {
		t.Fatalf("MedoidID = %q, want empty for nil index", clusters[0].MedoidID)
	}

	// Empty (non-nil) index with members absent → also untouched.
	clusters2 := []ThoughtCluster{{ID: "y", ThoughtIDs: []string{"a"}}}
	ComputeClusterCentroids(clusters2, map[string][]byte{})
	if clusters2[0].Centroid != nil || clusters2[0].MedoidID != "" {
		t.Fatalf("empty index: Centroid=%x MedoidID=%q, want nil/empty", clusters2[0].Centroid, clusters2[0].MedoidID)
	}
}

// TestBitSimilarity asserts identity is 1.0 and a known-bit-difference pair
// equals 1 - diff/256.
func TestBitSimilarity(t *testing.T) {
	v := bitVec(0, 7, 100, 255)
	if s := BitSimilarity(v, v); s != 1.0 {
		t.Fatalf("BitSimilarity(v,v) = %v, want 1.0", s)
	}

	cases := []struct {
		name string
		a    []byte
		b    []byte
		want float64
	}{
		{"one-bit", bitVec(), bitVec(0), 1 - 1.0/256},
		{"four-bits", bitVec(), bitVec(0, 8, 16, 24), 1 - 4.0/256},
		{"all-bits", vec32(0x00), vec32(0xFF), 1 - 256.0/256}, // 0.0
		{"nibble-per-byte", vec32(0x00), vec32(0x0F), 1 - 128.0/256},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if s := BitSimilarity(tc.a, tc.b); s != tc.want {
				t.Fatalf("BitSimilarity = %v, want %v", s, tc.want)
			}
		})
	}
}

// TestBitSimilarity_BadLength asserts a length mismatch yields 0 without panic.
func TestBitSimilarity_BadLength(t *testing.T) {
	if s := BitSimilarity(bitVec(0), []byte{0x01}); s != 0 {
		t.Fatalf("BitSimilarity(mismatched) = %v, want 0", s)
	}
	if s := BitSimilarity(nil, nil); s != 0 {
		t.Fatalf("BitSimilarity(nil,nil) = %v, want 0", s)
	}
}
