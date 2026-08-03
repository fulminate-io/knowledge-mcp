package searchengine

import (
	"crypto/sha256"
	"encoding/binary"
)

// maxBucketCount caps the derived bucket count. It bounds search fan-out: every
// bucket is a separate segment the read path queries in parallel, so an
// unbounded count would trade re-emit size for per-query cost.
const maxBucketCount = 1024

// BucketOf assigns a document id to one of bucketCount buckets by hashing the
// id. A bucketCount of 0 or 1 returns bucket 0 (one bucket holds everything),
// which also keeps the modulo below well defined.
//
// The assignment HASHES rather than partitioning the id space by range because
// document ids are heterogeneous across graph types: some are uniform random
// hex, but others — code symbols in particular — are structured paths that
// share long prefixes by construction, so a range partition would drop a whole
// directory into a single bucket. Hashing spreads both shapes evenly.
//
// The leading 8 bytes of the digest are ample: they are uniformly distributed,
// and only their residue modulo the count is used.
func BucketOf(id ExternalID, bucketCount int) int {
	if bucketCount <= 1 {
		return 0
	}
	sum := sha256.Sum256([]byte(id))
	return int(binary.BigEndian.Uint64(sum[:8]) % uint64(bucketCount))
}

// BucketCountFor derives the bucket count for a corpus of corpusDocs documents:
// the smallest power of two at or above the number of buckets needed to hold
// DefaultMinSegmentDocs documents each, clamped to [1, maxBucketCount]. A
// corpusDocs of 0 or less returns 1.
//
// The division is a CEILING: a corpus one document past an exact multiple needs
// one more bucket, not the same number.
//
// Powers of two make a count change a clean extension of the same hash and keep
// the count stable under small corpus jitter — a corpus drifting by a few
// hundred documents keeps its count rather than re-bucketing on every rebuild.
//
// THE DOUBLING EVENT is expected behavior, not a fault, and it is handled by
// write-driven realignment rather than by re-emitting everything at once. Because
// the count is derived from corpus size and BucketOf is a modulo of the hash,
// crossing a power-of-two boundary reassigns documents: powers of two mean each
// doubling reveals exactly one more bit of a member's hash, so a bucket under the
// old count splits into the buckets its members now hash to, and a segment one
// count behind spans two of them.
//
// Realignment then happens as writes arrive. A re-emit realigns the partitions it
// touches, together with whatever else their segments hold; a partition no write
// reaches keeps its old alignment until one does. That state is correct at every
// moment — every document stays live, in exactly one segment, and reachable — and
// it keeps the cost of a crossing proportional to the writes that cross it. Full
// realignment arrives as traffic covers the graph, or in one pass when the batch
// rebuild driver runs.
//
// A segment can therefore sit SEVERAL counts behind, spanning one partition per
// doubling it has missed. Anything deriving the partitions a segment occupies must
// walk its members rather than compute siblings arithmetically, or it will find
// some of them and silently miss the rest.
func BucketCountFor(corpusDocs int) int {
	if corpusDocs <= 0 {
		return 1
	}
	// Ceiling division written as quotient-plus-remainder so it cannot overflow
	// on a large corpusDocs the way adding the divisor first would.
	needed := corpusDocs / DefaultMinSegmentDocs
	if corpusDocs%DefaultMinSegmentDocs != 0 {
		needed++
	}
	count := 1
	for count < needed && count < maxBucketCount {
		count <<= 1
	}
	return count
}
