// SPDX-License-Identifier: Apache-2.0

package searchengine

import (
	"slices"
	"testing"
)

// bucket_group_scoped_harvest_test.go proves the NARROWING IS OUTPUT-IDENTICAL:
// handing a partition only the constituents that span it publishes exactly the
// bytes handing it the whole resolved union would have.
//
// WHY THAT IS THE WHOLE CLAIM. A segment id is a content hash, so equal ids IS
// byte-identical output — there is nothing weaker about asserting ids than
// comparing payloads, and it is the same equality the group's own reclaim logic
// relies on.
//
// BOTH ARMS ARE THE SAME FUNCTION WITH DIFFERENT FIRST ARGUMENTS. harvestPartition
// is the unit the narrowing changed and this test lives in its package, so no
// injection seam is needed and none is added: a second production path existing
// only to be compared against would be more surface proving less.

// scopedSpans builds the membership span map ReplaceBucketGroup builds, for the
// supplied entries. It is the INPUT to entriesSpanningBucket, never the expected
// output, so computing it the same way the production path does is not a tautology.
func scopedSpans[Q, S any](entries []*segmentEntry[Q, S], bucketCount int) map[SegmentID]map[int]bool {
	spans := make(map[SegmentID]map[int]bool, len(entries))
	for _, entry := range entries {
		held := make(map[int]bool)
		for id := range entry.members {
			held[BucketOf(id, bucketCount)] = true
		}
		spans[entry.meta.ID] = held
	}
	return spans
}

// TestScopedHarvestPublishesIdenticalContentHashes is the correctness keystone for
// the delete group-merge narrowing.
//
// THE FIXTURE VARIES THE AXES THE CLAIM DISCRIMINATES ON, because a fixture pair
// proves only the axes it varies:
//
//	(a) a BUCKET-ALIGNED constituent, the case the narrowing removes from every
//	    other partition;
//	(b) a CORPUS-SPANNING constituent, which must STILL be offered to each
//	    partition it spans — most of the real corpus's ~131-segment union;
//	(c) ORDER PRESERVATION, asserted DIRECTLY on entriesSpanningBucket rather than
//	    inferred from a merge winner. See the sub-test for why the indirect route is
//	    not available in this package;
//	(d) a partition with a superseded id and NO incoming documents — the pure-delete
//	    shape this ticket is actually about.
func TestScopedHarvestPublishesIdenticalContentHashes(t *testing.T) {
	const bucketCount = 2

	e := closeOnCleanup(t, New[mockQuery, mockStats](mockFormat{}, Options{
		MinSegmentDocs:     1 << 20, // only explicit Flush seals
		DeletesPctAllowed:  MergeDisabledDeadRatio,
		SegmentCountTarget: MergeDisabledCountTarget,
	}))

	b0 := groupIDsFor(t, 0, 12)
	b1 := groupIDsFor(t, 1, 12)

	// (a) aligned to partition 0; (b) spanning both; plus one aligned to partition 1
	// so BOTH partitions' shares hold more than one constituent and the order
	// assertion in (c) has something to be wrong about.
	aligned := sealSegment(t, e, b0[0:6], "alpha")
	spanning := sealSegment(t, e, append(append([]string{}, b0[6:9]...), b1[0:3]...), "beta")
	p1Only := sealSegment(t, e, b1[3:9], "gamma")

	set := e.set.Load()
	ids := []SegmentID{aligned, spanning, p1Only}
	// SORTED BY ID, mirroring the manager, which sorts each partition's constituent
	// list so the merge's last-copy-wins choice is arbitrary but STABLE.
	slices.Sort(ids)
	resolved := make([]*segmentEntry[mockQuery, mockStats], 0, len(ids))
	for _, id := range ids {
		entry := set.entryByID(id)
		if entry == nil {
			t.Fatalf("DEGENERATE FIXTURE: constituent %s is not resident", id[:12])
		}
		resolved = append(resolved, entry)
	}
	spans := scopedSpans(resolved, bucketCount)

	// FIXTURE PRECONDITIONS, asserted rather than assumed. Each names the axis it
	// protects, so a fixture that stopped exercising an axis reads as a broken probe
	// instead of silently making a sub-test vacuous.
	if !spans[aligned][0] || spans[aligned][1] {
		t.Fatalf("DEGENERATE FIXTURE: the aligned constituent must span partition 0 ONLY, got %v", spans[aligned])
	}
	if !spans[p1Only][1] || spans[p1Only][0] {
		t.Fatalf("DEGENERATE FIXTURE: the partition-1 constituent must span partition 1 ONLY, got %v", spans[p1Only])
	}
	if !spans[spanning][0] || !spans[spanning][1] {
		t.Fatalf("DEGENERATE FIXTURE: the spanning constituent must span BOTH partitions, got %v", spans[spanning])
	}
	// THE NARROWING MUST ACTUALLY NARROW SOMEWHERE, or every comparison below is a
	// slice compared with itself.
	for _, b := range []int{0, 1} {
		if got := len(entriesSpanningBucket(resolved, spans, b)); got != 2 {
			t.Fatalf("DEGENERATE FIXTURE: partition %d's scoped share is %d of %d constituents, want a strict subset of size 2",
				b, got, len(resolved))
		}
	}

	sameOutput := func(t *testing.T, w BucketWork) {
		t.Helper()
		unionEntry, unionFresh, err := e.harvestPartition(resolved, w, bucketCount)
		if err != nil {
			t.Fatalf("union arm: %v", err)
		}
		scopedEntry, scopedFresh, err := e.harvestPartition(entriesSpanningBucket(resolved, spans, w.Bucket), w, bucketCount)
		if err != nil {
			t.Fatalf("scoped arm: %v", err)
		}
		if unionEntry == nil && scopedEntry == nil {
			t.Fatal("both arms harvested nothing — this comparison proves nothing; the fixture must give this partition something to merge")
		}
		if (unionEntry == nil) != (scopedEntry == nil) {
			t.Fatalf("arms disagree on whether partition %d harvested anything: union nil=%v, scoped nil=%v",
				w.Bucket, unionEntry == nil, scopedEntry == nil)
		}
		if unionEntry.meta.ID != scopedEntry.meta.ID {
			t.Fatalf("partition %d: scoped harvest published %s but the union harvest published %s — a segment id is a content hash, so the narrowing changed the BYTES",
				w.Bucket, scopedEntry.meta.ID[:12], unionEntry.meta.ID[:12])
		}
		if unionFresh != scopedFresh {
			t.Fatalf("partition %d: fresh-build id differs between arms (%q vs %q)", w.Bucket, unionFresh, scopedFresh)
		}
	}

	t.Run("aligned constituent is dropped from the partition it does not span", func(t *testing.T) {
		for _, entry := range entriesSpanningBucket(resolved, spans, 1) {
			if entry.meta.ID == aligned {
				t.Fatal("the partition-0-aligned constituent was offered to partition 1")
			}
		}
		sameOutput(t, BucketWork{Bucket: 1})
	})

	t.Run("spanning constituent is still offered to every partition it spans", func(t *testing.T) {
		for _, b := range []int{0, 1} {
			found := false
			for _, entry := range entriesSpanningBucket(resolved, spans, b) {
				if entry.meta.ID == spanning {
					found = true
				}
			}
			if !found {
				t.Fatalf("the corpus-spanning constituent was withheld from partition %d it holds members of — that is data loss, not narrowing", b)
			}
		}
		sameOutput(t, BucketWork{Bucket: 0})
	})

	t.Run("the scoped share preserves the resolved order", func(t *testing.T) {
		// THIS ASSERTS THE ORDER CONTRACT DIRECTLY, and it is a DELIBERATE
		// SUBSTITUTION for the plan's axis (c), which asked for a document carried by
		// two constituents so that the merge's last-copy-wins choice would expose an
		// order-losing filter through a differing content hash.
		//
		// THAT ROUTE IS NOT AVAILABLE IN THIS PACKAGE. mockFormat.Merge concatenates
		// its constituents' rows and does not deduplicate shared ids
		// (mockformat_test.go), so a merge over two constituents carrying the same id
		// produces a segment with more nodes than distinct ids, which newEntry
		// REJECTS by design (engine.go:219, "a built index must carry exactly one node
		// per id"). Both arms therefore error identically and the comparison proves
		// nothing — reproduced before this substitution was made.
		//
		// The direct assertion is strictly stronger anyway: it pins the property an
		// order-losing filter violates, instead of a downstream consequence that only
		// shows up if the format happens to dedupe by last-copy-wins.
		for _, b := range []int{0, 1} {
			share := entriesSpanningBucket(resolved, spans, b)
			at := 0
			for _, entry := range share {
				for at < len(resolved) && resolved[at] != entry {
					at++
				}
				if at == len(resolved) {
					t.Fatalf("partition %d's scoped share is not an order-preserving subsequence of resolved — the merge keeps the LAST copy of a repeated id, so a reordered share changes which copy survives, in a path whose contract is byte reproducibility", b)
				}
				at++
			}
		}
	})

	t.Run("pure delete: superseded ids, no incoming documents", func(t *testing.T) {
		// STAGE LIVENESS THE WAY THE GROUP DOES. ReplaceBucketGroup kills the
		// superseded ids across the WHOLE resident set before any harvest, so a
		// direct harvestPartition call must do the same or the delete axis is
		// exercised against the wrong liveness and proves nothing.
		//
		// This sub-test runs LAST because the kill mutates the fixture's live bits
		// in place and does not undo.
		dead := []ExternalID{b0[1]}
		killSuperseded(set, dead)
		sameOutput(t, BucketWork{Bucket: 0, Superseded: dead})
	})
}
