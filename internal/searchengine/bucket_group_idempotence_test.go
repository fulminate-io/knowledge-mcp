// SPDX-License-Identifier: Apache-2.0

// bucket_group_idempotence_test.go gates the fuse property the local diff
// catch-up loop rests on: re-applying the SAME delta window through
// ReplaceBucketGroup republishes identical content per partition and leaves no
// duplicate beside the original.
//
// PACKAGE searchengine_test, NOT searchengine, and that is required rather than
// stylistic. This test must drive the REAL hnsw format, and formats/hnsw imports
// searchengine, so an IN-package test file could not import it without a cycle.
// An external test package in the same directory has no cycle. In-tree precedent
// for the idiom: internal/collector/collect_gate_identity_test.go (package
// collector_test), internal/llm/integration_test.go (package llm_test).
//
// hnsw AND NOT mockFormat, because with the mock the claim would not be about the
// fuse. mockFormat.Merge concatenates rows in constituent order and
// mockSegment.Encode is order-sensitive JSON, so a mock-backed idempotence result
// would be a statement about concatenation order. hnsw's Merge is explicitly
// order-independent: the doc block above Merge in formats/hnsw/format.go states
// "it is ORDER-INDEPENDENT: the builder sorts by id, so the result does not depend
// on which order the inputs were visited in, which is what makes a repeated
// consolidation idempotent even when the survivor set is assembled differently."
// That sentence is the property under test.
package searchengine_test

import (
	"fmt"
	"math/rand"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/hnsw"
)

const (
	// idempotenceBucketCount and idempotencePerBucket size the fixture SMALL on
	// purpose. This is a correctness test, and the measured serial hnsw build is
	// ~416ms/op at n=1024, so a 1024-per-bucket fixture would put seconds into
	// make test for no added discrimination.
	idempotenceBucketCount = 4
	idempotencePerBucket   = 32

	// idempotenceVecBytes mirrors the hnsw format's defaultVecBytes (32 — 256-bit
	// ubinary). That constant is unexported by its package, so it is restated here
	// rather than imported; a vector of any other width is not what the format
	// indexes.
	idempotenceVecBytes = 32
)

// fuseVector draws a vector from a CALLER-SUPPLIED, fixed-seed generator rather
// than crypto/rand, so a failure reproduces exactly on re-run.
func fuseVector(rng *rand.Rand) []byte {
	v := make([]byte, idempotenceVecBytes)
	for i := range v {
		v[i] = byte(rng.Intn(256))
	}
	return v
}

// fuseIDsByBucket returns perBucket ids for every partition under bucketCount,
// drawn from a deterministic id stream, together with the flat list in generation
// order.
func fuseIDsByBucket(t *testing.T, bucketCount, perBucket int) (map[int][]string, []string) {
	t.Helper()
	byBucket := make(map[int][]string, bucketCount)
	flat := make([]string, 0, bucketCount*perBucket)
	for i := 0; len(flat) < cap(flat); i++ {
		if i > 100000 {
			t.Fatalf("could not fill %d buckets with %d ids each", bucketCount, perBucket)
		}
		id := fmt.Sprintf("fuse-%05d", i)
		b := searchengine.BucketOf(id, bucketCount)
		if len(byBucket[b]) >= perBucket {
			continue
		}
		byBucket[b] = append(byBucket[b], id)
		flat = append(flat, id)
	}
	return byBucket, flat
}

// residentIDs returns the resident segment ids as a set.
func residentIDs(e *searchengine.SegmentedIndex[[]byte, struct{}]) map[searchengine.SegmentID]bool {
	out := map[searchengine.SegmentID]bool{}
	for _, b := range e.Export() {
		out[b.ID] = true
	}
	return out
}

// TestBucketGroupFuseIsIdempotentOnRepeatedApply drives ReplaceBucketGroup TWICE
// over the same delta window and pins that the second apply republishes the same
// content hash per partition and adds nothing beside the original.
//
// THIS IS THE PROPERTY THE CATCH-UP LOOP RESTS ON. The loop re-applies a delta
// window whenever a watermark was not committed — a crash between the fuse and
// the watermark advance is the ordinary case, not the exotic one — so a fuse that
// published a fresh generation on re-apply would grow the corpus once per retry.
//
// WHY THE SECOND APPLY DOES NOT PRODUCE DUPLICATE MEMBERS, stated so a reader
// expects it rather than debugs it: ReplaceBucketGroup kills the whole group's
// superseded ids BEFORE any partition harvests, so on the second apply the
// resident copies are dead and acceptLiveMembers rejects them from every
// constituent; only the freshly built segment carries them.
func TestBucketGroupFuseIsIdempotentOnRepeatedApply(t *testing.T) {
	eng := searchengine.New[[]byte, struct{}](hnsw.New(), searchengine.Options{
		// Only an explicit Flush seals, and the background merge triggers are
		// disarmed with the same values the production owner passes
		// (segmentdist/manager_factory.go), so nothing consolidates across bucket
		// boundaries mid-test.
		MinSegmentDocs:     1 << 20,
		DeletesPctAllowed:  searchengine.MergeDisabledDeadRatio,
		SegmentCountTarget: searchengine.MergeDisabledCountTarget,
	})
	defer eng.Close()

	byBucket, flat := fuseIDsByBucket(t, idempotenceBucketCount, idempotencePerBucket)

	// (1) Seed a resident corpus in ONE segment spanning every partition — the
	// shape a GROUP swap exists for, since a spanning segment's members belong to
	// more than one partition.
	seedRNG := rand.New(rand.NewSource(20260826))
	seed := make([]searchengine.Document, 0, len(flat))
	for _, id := range flat {
		seed = append(seed, searchengine.Document{ID: id, Vector: fuseVector(seedRNG)})
	}
	if err := eng.Add(seed); err != nil {
		t.Fatalf("Add seed corpus: %v", err)
	}
	if err := eng.Flush(); err != nil {
		t.Fatalf("Flush seed corpus: %v", err)
	}

	// (2) The delta window: NEWER copies of every seeded id, carrying genuinely
	// different vectors, routed to their partitions by BucketOf. Superseded is the
	// window's own ids, which mirrors production — the drain computes
	// ids := docIDs(docs) and passes them as the superseded set.
	deltaRNG := rand.New(rand.NewSource(19700101))
	work := make([]searchengine.BucketWork, 0, idempotenceBucketCount)
	for b := range idempotenceBucketCount {
		ids := byBucket[b]
		docs := make([]searchengine.Document, 0, len(ids))
		for _, id := range ids {
			docs = append(docs, searchengine.Document{ID: id, Vector: fuseVector(deltaRNG)})
		}
		work = append(work, searchengine.BucketWork{
			Bucket:     b,
			Superseded: append([]searchengine.ExternalID(nil), ids...),
			Docs:       docs,
		})
	}

	constituents := func() []searchengine.SegmentID {
		var out []searchengine.SegmentID
		for _, b := range eng.Export() {
			out = append(out, b.ID)
		}
		return out
	}

	// (3) APPLY ONCE.
	want, _, err := eng.ReplaceBucketGroup(idempotenceBucketCount, constituents(), work)
	if err != nil {
		t.Fatalf("first ReplaceBucketGroup: %v", err)
	}
	// KNOWN-POSITIVE CONTROL. Without it two empty maps compare equal and the
	// whole test passes having fused nothing — a fixture that published nothing is
	// indistinguishable from an idempotent one.
	if len(want) != idempotenceBucketCount {
		t.Fatalf("first apply published %d partitions, want %d — the fixture fused nothing, so the comparison below would be vacuous", len(want), idempotenceBucketCount)
	}
	for b, id := range want {
		if id == "" {
			t.Fatalf("first apply published an empty id for partition %d", b)
		}
	}
	residentAfterFirst := residentIDs(eng)
	distinctAfterFirst := eng.DistinctResidentDocCount()
	if distinctAfterFirst != len(flat) {
		t.Fatalf("after the first apply the corpus holds %d distinct documents, want the %d seeded", distinctAfterFirst, len(flat))
	}

	// (4) APPLY AGAIN with the SAME work, re-resolving the constituents against the
	// set the first apply published.
	got, _, err := eng.ReplaceBucketGroup(idempotenceBucketCount, constituents(), work)
	if err != nil {
		t.Fatalf("second ReplaceBucketGroup: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("second apply published %d partitions, first published %d", len(got), len(want))
	}
	for b, wantID := range want {
		gotID, ok := got[b]
		if !ok {
			t.Fatalf("second apply published nothing for partition %d; the first published %s", b, wantID[:12])
		}
		if gotID != wantID {
			t.Fatalf("partition %d republished a DIFFERENT content hash on re-apply: first %s, second %s — the fuse is not idempotent, so every retried delta window would mint a fresh generation",
				b, wantID[:12], gotID[:12])
		}
	}

	// (5) A re-applied delta must not leave a duplicate BESIDE the original: the
	// resident id set is unchanged, not merely a superset.
	residentAfterSecond := residentIDs(eng)
	if len(residentAfterSecond) != len(residentAfterFirst) {
		t.Fatalf("the re-applied window changed the resident segment count: %d after the first apply, %d after the second", len(residentAfterFirst), len(residentAfterSecond))
	}
	for id := range residentAfterFirst {
		if !residentAfterSecond[id] {
			t.Fatalf("segment %s was resident after the first apply and is gone after the re-apply", id[:12])
		}
	}
	if distinct := eng.DistinctResidentDocCount(); distinct != len(flat) {
		t.Fatalf("after the re-apply the corpus holds %d distinct documents, want the %d seeded", distinct, len(flat))
	}
}
