package searchengine

import (
	"fmt"
	"strings"
	"testing"
)

// duplicate_layer_ledger_test.go is INSTRUMENTATION, not a gate. It records, for
// each ReplaceBucket call over a corpus whose ids live in more than one segment:
// how many members each constituent offered, how many the accept predicate
// admitted, how many survived Merge into the output, and what the output's
// DocCount finally reports — plus, between calls, whether the NEXT partition's
// constituents are still resident.
//
// It is in-package because those columns are engine internals: entry.members,
// entry.live and the accept predicate are unexported, and reading them from a test
// is the whole point of putting the ledger here.

const ledgerBucketCount = 2

// ledgerIDs returns n ids together with the partition each hashes into under
// ledgerBucketCount, so the fixture can assert it genuinely spans both.
func ledgerIDs(n int) (ids []string, perBucket map[int]int) {
	perBucket = map[int]int{}
	for i := range n {
		id := fmt.Sprintf("doc-%05d", i)
		ids = append(ids, id)
		perBucket[BucketOf(id, ledgerBucketCount)]++
	}
	return ids, perBucket
}

// oneSegmentPerLayer builds a single sealed segment holding every id, which is the
// shape a segment aligned to a SMALLER partition count has when read at a larger
// one: its members span several partitions.
func oneSegmentPerLayer(t *testing.T, e *SegmentedIndex[mockQuery, mockStats], ids []string, contentSalt string) {
	t.Helper()
	docs := make([]Document, 0, len(ids))
	for _, id := range ids {
		docs = append(docs, doc(id, "tok "+contentSalt+" "+id))
	}
	if err := e.Add(docs); err != nil {
		t.Fatalf("Add layer %s: %v", contentSalt, err)
	}
	if err := e.Flush(); err != nil {
		t.Fatalf("Flush layer %s: %v", contentSalt, err)
	}
}

// distinctResident counts the DISTINCT live member ids across the resident set —
// the quantity the corpus is supposed to preserve, and the one ResidentDocCount
// cannot report once segments hold duplicate ids.
func distinctResident(e *SegmentedIndex[mockQuery, mockStats]) map[string]bool {
	set := e.set.Load()
	out := map[string]bool{}
	for _, entry := range set.entries {
		for id, ord := range entry.members {
			if entry.live.Live(ord) {
				out[id] = true
			}
		}
	}
	return out
}

// TestDuplicateLayerLedger drives the two partitions of a duplicated corpus one at
// a time and prints the per-call ledger. It asserts only the fixture's own shape;
// the numbers it prints are the evidence the investigation reads.
func TestDuplicateLayerLedger(t *testing.T) {
	const corpus = 64

	ids, perBucket := ledgerIDs(corpus)
	if perBucket[0] == 0 || perBucket[1] == 0 {
		t.Fatalf("fixture must span both partitions, got %v", perBucket)
	}
	t.Logf("FIXTURE %d distinct ids, partition split: bucket0=%d bucket1=%d",
		corpus, perBucket[0], perBucket[1])

	// One engine holding TWO layers over the SAME ids with different content — what
	// two rebuilds at different times leave behind. MinSegmentDocs is above the
	// corpus so only the explicit Flush seals, giving one segment per layer.
	e := closeOnCleanup(t, New[mockQuery, mockStats](mockFormat{}, Options{
		MinSegmentDocs:     corpus * 4,
		DeletesPctAllowed:  MergeDisabledDeadRatio,
		SegmentCountTarget: MergeDisabledCountTarget,
	}))
	defer e.Close()

	oneSegmentPerLayer(t, e, ids, "layerA")
	oneSegmentPerLayer(t, e, ids, "layerB")

	before := e.Export()
	if len(before) != 2 {
		t.Fatalf("fixture wants 2 layered segments, got %d", len(before))
	}
	constituents := []SegmentID{before[0].ID, before[1].ID}
	t.Logf("LAYERED segments=%d residentDocCount=%d distinctLiveMembers=%d (distinct corpus is %d)",
		len(before), e.ResidentDocCount(), len(distinctResident(e)), corpus)

	// Both partitions are in the rebuild set and both name the SAME two
	// constituents — the closure is satisfied by construction here, which is what
	// isolates the defect from the closure.
	for _, bucket := range []int{0, 1} {
		set := e.set.Load()

		resolvedCount := 0
		for _, cid := range constituents {
			entry := set.entryByID(cid)
			if entry == nil {
				t.Logf("  bucket %d | constituent %s NOT RESIDENT (already consumed)", bucket, cid[:12])
				continue
			}
			resolvedCount++
			offered, admitted := 0, 0
			accept := acceptLiveMembers(entry, bucket, ledgerBucketCount)
			for id := range entry.members {
				offered++
				if accept(id) {
					admitted++
				}
			}
			t.Logf("  bucket %d | constituent %s offered=%d admitted=%d", bucket, cid[:12], offered, admitted)
		}

		// THE DUPLICATION THIS LEDGER RECORDED IS NOW REJECTED AT CONSTRUCTION, and the
		// refusal is the corrected behavior rather than a regression. mockFormat.Merge
		// deliberately does NOT deduplicate its items — a mock that repaired itself
		// would make the invariant gate vacuous, which is the trap the real gate was
		// moved out of this file to escape — so merging constituents that share ids
		// still yields a segment carrying two nodes per id, and newEntry now refuses to
		// publish it. This test therefore keeps its instrumentation of the pre-merge
		// state and asserts the REFUSAL where it used to record a 70-for-35 swap.
		published, err := e.ReplaceBucket(bucket, ledgerBucketCount, constituents, nil, nil)
		if err == nil {
			t.Fatalf("ReplaceBucket(%d): expected the graph-equals-members invariant to REFUSE a merge over constituents that share ids, got a successful swap publishing %q", bucket, published)
		}
		if !strings.Contains(err.Error(), "must carry exactly one node per id") {
			t.Fatalf("ReplaceBucket(%d): expected the graph-equals-members invariant error, got: %v", bucket, err)
		}
		if published != "" {
			t.Fatalf("ReplaceBucket(%d): a refused swap must publish nothing, got %q", bucket, published)
		}
		t.Logf("  bucket %d REFUSED as designed: %v", bucket, err)

		survived, docCount := 0, 0
		if published != "" {
			entry := e.set.Load().entryByID(published)
			if entry != nil {
				survived = len(entry.members)
				docCount = entry.meta.DocCount
			}
		}
		t.Logf("BUCKET %d LEDGER resolvedConstituents=%d published=%q survivedDistinct=%d finalDocCount=%d",
			bucket, resolvedCount, shortID(published), survived, docCount)
		t.Logf("  after bucket %d: segments=%d residentDocCount=%d distinctLiveMembers=%d",
			bucket, len(e.Export()), e.ResidentDocCount(), len(distinctResident(e)))
	}

	live := distinctResident(e)
	missing := 0
	for _, id := range ids {
		if !live[id] {
			missing++
		}
	}
	t.Logf("VERDICT distinctLiveMembers=%d of %d  missing=%d  segments=%d",
		len(live), corpus, missing, len(e.Export()))
}

// shortID trims a content-hash id for log readability.
func shortID(id SegmentID) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
