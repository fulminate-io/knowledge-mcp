package searchengine

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"testing"
)

// hexIDs builds n uniform 32-hex-character ids — the shape a graph mints for
// knowledge and thought nodes (16 random bytes, hex encoded). Derived from a
// counter digest rather than a PRNG so the corpus is identical on every run.
func hexIDs(n int) []ExternalID {
	ids := make([]ExternalID, n)
	for i := range n {
		sum := sha256.Sum256(fmt.Appendf(nil, "seed-%d", i))
		ids[i] = hex.EncodeToString(sum[:16])
	}
	return ids
}

// pathIDs builds n receiver-qualified path ids — the shape a code graph mints.
// They are deliberately drawn from only a handful of directories so the corpus
// is heavily prefix-clustered: this is the input a range partition over the raw
// id would collapse into a few buckets.
func pathIDs(n int) []ExternalID {
	dirs := []string{
		"cmd/knowledge/internal/searchengine",
		"cmd/knowledge/internal/segmentdist",
		"cmd/knowledge/internal/tools",
		"cmd/knowledge-server/internal/store",
	}
	ids := make([]ExternalID, n)
	for i := range n {
		dir := dirs[i%len(dirs)]
		ids[i] = fmt.Sprintf("%s/file%d.go:Type%d.Method%d", dir, i%37, i%11, i)
	}
	return ids
}

// TestBucketOfDeterministicAndUniform pins the two properties the partition
// rests on: the same id always lands in the same bucket, and both id shapes
// spread evenly — including the prefix-clustered path ids, which are the reason
// the assignment hashes instead of slicing the id range. Degenerate counts
// collapse to bucket 0 rather than dividing by zero.
func TestBucketOfDeterministicAndUniform(t *testing.T) {
	const (
		corpus  = 20000
		buckets = 64
	)

	corpora := map[string][]ExternalID{
		"hex":  hexIDs(corpus),
		"path": pathIDs(corpus),
	}

	for shape, ids := range corpora {
		counts := make([]int, buckets)
		for _, id := range ids {
			b := BucketOf(id, buckets)
			if b < 0 || b >= buckets {
				t.Fatalf("%s: BucketOf(%q, %d) = %d, want in [0,%d)", shape, id, buckets, b, buckets)
			}
			if again := BucketOf(id, buckets); again != b {
				t.Fatalf("%s: BucketOf(%q, %d) not deterministic: %d then %d", shape, id, buckets, b, again)
			}
			counts[b]++
		}

		// No bucket above 2x the mean — the prefix-clustering guard.
		mean := float64(corpus) / float64(buckets)
		limit := 2 * mean
		for b, got := range counts {
			if float64(got) > limit {
				t.Errorf("%s: bucket %d holds %d ids, above the 2x-mean limit of %.1f (mean %.1f)",
					shape, b, got, limit, mean)
			}
			if got == 0 {
				t.Errorf("%s: bucket %d is empty over %d ids", shape, b, corpus)
			}
		}
	}

	// A changed count re-assigns ids, but the assignment stays in range.
	for _, id := range hexIDs(100) {
		if b := BucketOf(id, 128); b < 0 || b >= 128 {
			t.Fatalf("BucketOf(%q, 128) = %d, out of range", id, b)
		}
	}

	// Degenerate counts: one bucket holds everything, no modulo by zero.
	for _, count := range []int{0, 1} {
		for _, id := range []ExternalID{"", "a", "cmd/knowledge/internal/searchengine/engine.go:SegmentedIndex.Delete"} {
			if b := BucketOf(id, count); b != 0 {
				t.Errorf("BucketOf(%q, %d) = %d, want 0", id, count, b)
			}
		}
	}
}

// TestBucketCountForTargetsMinSegmentDocs pins the derived count, including the
// boundary pair that gates the arithmetic: 131072 documents divide exactly into
// 128 buckets of DefaultMinSegmentDocs, and one more document needs 129, which
// rounds up to 256. Integer truncation would answer 128 for both.
func TestBucketCountForTargetsMinSegmentDocs(t *testing.T) {
	cases := []struct {
		corpusDocs int
		want       int
	}{
		{corpusDocs: -1, want: 1},
		{corpusDocs: 0, want: 1},
		{corpusDocs: 1, want: 1},
		{corpusDocs: 300, want: 1},
		{corpusDocs: DefaultMinSegmentDocs, want: 1},
		{corpusDocs: DefaultMinSegmentDocs + 1, want: 2},
		{corpusDocs: 4096, want: 4},
		{corpusDocs: 16384, want: 16},
		{corpusDocs: 97000, want: 128},
		// The ceiling gate: exact multiple, then one document past it.
		{corpusDocs: 131072, want: 128},
		{corpusDocs: 131073, want: 256},
		// Clamped at the cap.
		{corpusDocs: maxBucketCount * DefaultMinSegmentDocs, want: maxBucketCount},
		{corpusDocs: 100 * maxBucketCount * DefaultMinSegmentDocs, want: maxBucketCount},
	}
	for _, tc := range cases {
		if got := BucketCountFor(tc.corpusDocs); got != tc.want {
			t.Errorf("BucketCountFor(%d) = %d, want %d", tc.corpusDocs, got, tc.want)
		}
	}

	// Every answer is a power of two within [1, maxBucketCount], sampled across
	// the range and around each doubling boundary.
	sizes := []int{0, 1, 2, 1023, 1025}
	for count := 1; count <= maxBucketCount; count *= 2 {
		edge := count * DefaultMinSegmentDocs
		sizes = append(sizes, edge-1, edge, edge+1)
	}
	sizes = append(sizes, 5_000_000)
	for _, size := range sizes {
		got := BucketCountFor(size)
		if got < 1 || got > maxBucketCount {
			t.Errorf("BucketCountFor(%d) = %d, outside [1,%d]", size, got, maxBucketCount)
		}
		if got&(got-1) != 0 {
			t.Errorf("BucketCountFor(%d) = %d, not a power of two", size, got)
		}
	}
}
