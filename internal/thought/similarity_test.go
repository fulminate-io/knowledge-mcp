// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"bytes"
	"math"
	"testing"
)

// TestGroupSimilarity_EmbeddingSource: group embedding prefers the topic-summary
// vector when present and falls back to the centroid otherwise.
func TestGroupSimilarity_EmbeddingSource(t *testing.T) {
	centroid := bitVec(0, 1, 2)
	summaryVec := bitVec(100, 101)

	// Topic WITH a summary vector → prefers it.
	withSummary := Topic{Centroid: centroid, SummaryVector: summaryVec}
	if got := groupEmbedding(withSummary); !bytes.Equal(got, summaryVec) {
		t.Fatalf("group embedding = %x, want the summary vector %x", got, summaryVec)
	}

	// Topic WITHOUT a summary vector → falls back to the centroid.
	noSummary := Topic{Centroid: centroid}
	if got := groupEmbedding(noSummary); !bytes.Equal(got, centroid) {
		t.Fatalf("group embedding = %x, want the centroid %x (fallback)", got, centroid)
	}

	// A wrong-length summary vector is ignored → centroid fallback.
	badSummary := Topic{Centroid: centroid, SummaryVector: []byte{0x01}}
	if got := groupEmbedding(badSummary); !bytes.Equal(got, centroid) {
		t.Fatalf("group embedding = %x, want centroid fallback on a bad summary vec", got)
	}
}

// TestRunGroupSimilarity_LinkClassification: over the surviving topics, a pair at
// or above the link threshold is a link candidate; a pair below is not.
func TestRunGroupSimilarity_LinkClassification(t *testing.T) {
	// Three topics. A and B are bit-close (sim high → above link threshold). C is
	// far from both (sim low → below). Build vectors so:
	//   sim(A,B) ~ 0.99  (differ in ~2 bits)
	//   sim(A,C), sim(B,C) ~ 0.5 (differ in ~128 bits)
	base := bitVec()  // all zero
	a := bitVec()     // all zero
	b := bitVec(0, 1) // differs from A in 2 bits → sim 254/256 ≈ 0.992
	c := make([]byte, vectorBytes)
	for i := range 16 {
		c[i] = 0xFF // 128 bits set → far from the all-zero A/B
	}
	_ = base

	topics := []Topic{
		{MedoidID: "mA", Centroid: a},
		{MedoidID: "mB", Centroid: b},
		{MedoidID: "mC", Centroid: c},
	}

	const linkThreshold = 0.90
	links := RunGroupSimilarity(topics, linkThreshold)

	// Exactly the A–B pair clears the link threshold.
	if len(links) != 1 {
		t.Fatalf("link candidates = %d, want 1 (only A–B clears %.2f)", len(links), linkThreshold)
	}
	lc := links[0]
	if !((lc.MedoidA == "mA" && lc.MedoidB == "mB") || (lc.MedoidA == "mB" && lc.MedoidB == "mA")) {
		t.Fatalf("link candidate medoids = (%s,%s), want the A–B pair", lc.MedoidA, lc.MedoidB)
	}
	if lc.Score < linkThreshold {
		t.Fatalf("link candidate score %.4f < threshold %.2f", lc.Score, linkThreshold)
	}

	// A pair below the threshold (A–C, B–C) must NOT be a candidate — already
	// asserted by len==1, but assert no candidate references mC.
	for _, l := range links {
		if l.MedoidA == "mC" || l.MedoidB == "mC" {
			t.Fatalf("below-threshold pair involving mC was classified as a link candidate")
		}
	}
}

// TestSurveyGroupSimilarity: the threshold-tuning survey buckets pairs ≥ 0.70
// into the histogram, lists below-link-threshold pairs as near misses (sorted
// desc), excludes pairs at/above the threshold from the near-miss list, and
// drops sub-floor pairs entirely.
func TestSurveyGroupSimilarity(t *testing.T) {
	a := bitVec() // all zero
	// B differs from A in 13 bits → sim = 1 - 13/256 ≈ 0.949: counted in the
	// [0.90, 0.95) bucket AND a near miss below link 0.96.
	b := bitVec(0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12)
	// D differs from A in 2 bits → sim ≈ 0.992: top bucket, NOT a near miss
	// (clears the threshold).
	d := bitVec(0, 1)
	// C is ~128 bits from A → sim ≈ 0.5: below the 0.70 floor, dropped.
	c := make([]byte, vectorBytes)
	for i := range 16 {
		c[i] = 0xFF
	}

	topics := []Topic{
		{MedoidID: "mA", Centroid: a},
		{MedoidID: "mB", Centroid: b},
		{MedoidID: "mC", Centroid: c},
		{MedoidID: "mD", Centroid: d},
	}

	const linkThreshold = 0.96
	buckets, near := SurveyGroupSimilarity(topics, linkThreshold)

	// Histogram totals — A–B (13 bits ≈ 0.949) → [0.90,0.95); A–D (2 bits ≈
	// 0.992) and B–D (11 bits — bits 0,1 overlap — ≈ 0.957) → [0.95,1.00);
	// every mC pair is sub-floor.
	total := 0
	var bucket9095, bucket95up int
	for _, bk := range buckets {
		total += bk.Count
		// Bucket bounds accumulate float error (0.70 + 4×0.05 ≠ exactly 0.90) —
		// match with a tolerance, never ==.
		switch {
		case math.Abs(bk.Lo-0.90) < 1e-9:
			bucket9095 = bk.Count
		case math.Abs(bk.Lo-0.95) < 1e-9:
			bucket95up = bk.Count
		}
	}
	if total != 3 {
		t.Fatalf("surveyed pairs = %d, want 3 (A–B, A–D, B–D; mC pairs are sub-floor)", total)
	}
	if bucket9095 != 1 {
		t.Fatalf("[0.90,0.95) count = %d, want 1 (A–B)", bucket9095)
	}
	if bucket95up != 2 {
		t.Fatalf("[0.95,1.00) count = %d, want 2 (A–D and B–D)", bucket95up)
	}

	// Near misses below 0.96: B–D (0.957) and A–B (0.949), sorted desc — B–D
	// first. A–D (0.992) clears the threshold and must be absent.
	if len(near) != 2 {
		t.Fatalf("near misses = %d, want 2 (B–D, A–B below %.2f; A–D clears it)", len(near), linkThreshold)
	}
	if near[0].Score < near[1].Score {
		t.Fatalf("near misses not sorted desc: %.4f then %.4f", near[0].Score, near[1].Score)
	}
	for _, nm := range near {
		if nm.Score >= linkThreshold {
			t.Fatalf("near miss at %.4f is >= the link threshold %.2f — that's a link, not a miss", nm.Score, linkThreshold)
		}
		if nm.MedoidA == "mC" || nm.MedoidB == "mC" {
			t.Fatalf("sub-floor pair involving mC surfaced as a near miss")
		}
	}
}
