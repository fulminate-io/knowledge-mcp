package searchengine

import (
	"fmt"
	"testing"
)

// bucketTestEngine builds a mock-format engine that seals one segment per Add and
// never background-merges, so a test owns the segment layout outright.
func bucketTestEngine(t testing.TB, onMerge OnMergeFunc) *SegmentedIndex[mockQuery, mockStats] {
	t.Helper()
	return closeOnCleanup(t, New[mockQuery, mockStats](mockFormat{}, Options{
		MinSegmentDocs:     1,
		DeletesPctAllowed:  MergeDisabledDeadRatio,
		SegmentCountTarget: MergeDisabledCountTarget,
		OnMerge:            onMerge,
	}))
}

// constituentsBucketCount is the partition width the lookup test assigns ids with.
const constituentsBucketCount = 8

// idForBucket returns the first id from a deterministic stream that hashes into
// the given bucket, skipping the ids already taken.
func idForBucket(t *testing.T, bucket int, taken map[string]bool) string {
	t.Helper()
	for i := range 100000 {
		id := fmt.Sprintf("doc-%05d", i)
		if taken[id] || BucketOf(id, constituentsBucketCount) != bucket {
			continue
		}
		taken[id] = true
		return id
	}
	t.Fatalf("no id found for bucket %d of %d", bucket, constituentsBucketCount)
	return ""
}

// TestReplaceBucketFiresReclaimHook pins the supersession event ReplaceBucket
// raises. The engine invokes the merge-completion callback at exactly one other
// place — the background merge — and an owner that drives its own segment layout
// runs with that trigger disarmed, so this is the only event it will receive. If
// it stopped firing, every re-emit would leave its predecessor's stored copy
// behind with nothing to reclaim it.
//
// The callback must carry the superseded constituent ids and a usable blob of the
// consolidated segment, and a nil callback must be a silent no-op.
func TestReplaceBucketFiresReclaimHook(t *testing.T) {
	t.Run("fires once with the superseded ids and the merged blob", func(t *testing.T) {
		var fired []MergeResult
		e := bucketTestEngine(t, func(res MergeResult) { fired = append(fired, res) })
		defer e.Close()

		if err := e.Add([]Document{doc("a", "alpha"), doc("b", "alpha beta")}); err != nil {
			t.Fatalf("Add: %v", err)
		}
		before := e.Export()
		if len(before) != 1 {
			t.Fatalf("Export before = %d segments, want 1", len(before))
		}
		constituent := before[0].ID

		// A SINGLE-PARTITION fixture: these ids are not assigned to any partition, and
		// a count of 1 puts every id in partition 0, so the accept predicate's
		// partition test admits them all. What is under test here is the reclaim hook.
		if _, err := e.ReplaceBucket(0, 1, []SegmentID{constituent}, nil, []Document{doc("c", "gamma")}); err != nil {
			t.Fatalf("ReplaceBucket: %v", err)
		}

		if len(fired) != 1 {
			t.Fatalf("the reclaim hook fired %d times, want exactly 1", len(fired))
		}
		res := fired[0]
		if len(res.Removed) != 1 || res.Removed[0] != constituent {
			t.Fatalf("MergeResult.Removed = %v, want [%s]", res.Removed, constituent)
		}
		after := e.Export()
		if len(after) != 1 {
			t.Fatalf("Export after = %d segments, want 1 consolidated", len(after))
		}
		if res.Merged.ID != after[0].ID {
			t.Fatalf("MergeResult.Merged.ID = %s, want the resident consolidated id %s", res.Merged.ID, after[0].ID)
		}
		if res.Merged.ID == constituent {
			t.Fatal("the consolidated segment must carry a new content hash")
		}
		if len(res.Merged.Bytes) == 0 {
			t.Fatal("MergeResult.Merged.Bytes is empty — the owner has nothing durable to store")
		}
		if want := (mockFormat{}).Name(); res.Merged.Format != want {
			t.Fatalf("MergeResult.Merged.Format = %q, want %q", res.Merged.Format, want)
		}
		if res.Merged.DocCount != 3 {
			t.Fatalf("MergeResult.Merged.DocCount = %d, want 3 (two carried plus one added)", res.Merged.DocCount)
		}
	})

	t.Run("a nil hook is a silent no-op", func(t *testing.T) {
		e := bucketTestEngine(t, nil)
		defer e.Close()

		if err := e.Add([]Document{doc("a", "alpha")}); err != nil {
			t.Fatalf("Add: %v", err)
		}
		before := e.Export()
		if len(before) != 1 {
			t.Fatalf("Export before = %d segments, want 1", len(before))
		}
		if _, err := e.ReplaceBucket(0, 1, []SegmentID{before[0].ID}, nil, []Document{doc("b", "beta")}); err != nil {
			t.Fatalf("ReplaceBucket with a nil hook: %v", err)
		}
		if after := e.Export(); len(after) != 1 {
			t.Fatalf("Export after = %d segments, want 1 consolidated", len(after))
		}
	})
}

// TestAddSealAndSupersedeOrdering pins the primitive's ordering contract, which is
// the whole reason it lives in the engine rather than in a caller.
//
// Four things must hold for a re-added id: it is returned by search THROUGHOUT
// (never absent, which a clear-then-seal order would cause); it ends with exactly
// ONE live copy; that copy carries the FRESH payload (a copy resolved after the
// seal would name the new segment, so clearing through it would spare the stale
// one instead — the failure this ordering exists to prevent); and the returned ids
// name the segment the seal actually produced, which the caller needs to retire
// that segment later.
func TestAddSealAndSupersedeOrdering(t *testing.T) {
	e := bucketTestEngine(t, nil)
	defer e.Close()

	// A resident segment holding the stale copy, plus a bystander.
	if err := e.Add([]Document{doc("re-added", "stale"), doc("bystander", "alpha")}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	before := e.Export()
	if len(before) != 1 {
		t.Fatalf("Export before = %d segments, want 1", len(before))
	}
	staleSegment := before[0].ID

	// The id is searchable before the call, and must remain so after it.
	if hits := e.Search(mockQuery{term: "stale"}, 10); len(hits) != 1 || hits[0].ID != "re-added" {
		t.Fatalf("pre-call Search(stale) = %v, want the stale copy", searchIDs(hits))
	}

	sealed, err := e.AddSealAndSupersede([]Document{doc("re-added", "fresh")})
	if err != nil {
		t.Fatalf("AddSealAndSupersede: %v", err)
	}

	// (1) Still returned by search — under the fresh term, and never twice.
	hits := e.Search(mockQuery{term: "fresh"}, 10)
	if len(hits) != 1 || hits[0].ID != "re-added" {
		t.Fatalf("post-call Search(fresh) = %v, want exactly the re-added id", searchIDs(hits))
	}

	// (2)+(3) Exactly ONE live copy, and it is the FRESH one. A clear resolved after
	// the seal would have killed the fresh copy and left the stale one answering.
	if stale := e.Search(mockQuery{term: "stale"}, 10); len(stale) != 0 {
		t.Fatalf("the pre-seal copy is still live: Search(stale) = %v", searchIDs(stale))
	}

	// The bystander in the same segment is untouched.
	if hits := e.Search(mockQuery{term: "alpha"}, 10); len(hits) != 1 || hits[0].ID != "bystander" {
		t.Fatalf("bystander lost: Search(alpha) = %v", searchIDs(hits))
	}

	// (4) The result names the segment the seal produced — a NEW id, resident, and
	// not the segment that held the stale copy — and reports that it created it.
	if !sealed.Created {
		t.Fatal("a batch with fresh content published a new segment and must report Created")
	}
	if sealed.ID == staleSegment {
		t.Fatal("the returned id names the pre-existing segment, not the sealed one")
	}
	var found bool
	for _, b := range e.Export() {
		if b.ID == sealed.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("returned id %s is not resident — it does not name a real sealed segment", sealed.ID)
	}

	// A first-time add contributes no victim and still reports its segment.
	fresh, err := e.AddSealAndSupersede([]Document{doc("brand-new", "gamma")})
	if err != nil {
		t.Fatalf("AddSealAndSupersede(first-time): %v", err)
	}
	if !fresh.Created || fresh.ID == sealed.ID {
		t.Fatalf("a first-time add must report its own new segment, got %+v", fresh)
	}
	if hits := e.Search(mockQuery{term: "gamma"}, 10); len(hits) != 1 {
		t.Fatalf("first-time add is not searchable: %v", searchIDs(hits))
	}
}

// TestBucketConstituentsFindsResidentBucket covers the three states a lookup meets:
// a bucket held by exactly one segment, a bucket no resident segment holds, and a
// bucket split across two segments — the shape a partial re-emit leaves behind and
// the one a caller must consolidate.
func TestBucketConstituentsFindsResidentBucket(t *testing.T) {
	taken := map[string]bool{}

	// Three ids in bucket 1 — two sharing ONE segment, the third in another, so the
	// result must be deduped per segment rather than per member — plus one in
	// bucket 3. Bucket 5 gets nothing.
	splitA := idForBucket(t, 1, taken)
	splitACompanion := idForBucket(t, 1, taken)
	splitB := idForBucket(t, 1, taken)
	lone := idForBucket(t, 3, taken)

	e := bucketTestEngine(t, nil)
	defer e.Close()

	// One Add per segment: splitA and its bucket companion land with lone, splitB
	// seals on its own, so bucket 1 spans two segments while bucket 3 sits in one.
	if err := e.Add([]Document{doc(splitA, "alpha"), doc(splitACompanion, "alpha"), doc(lone, "alpha")}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := e.Add([]Document{doc(splitB, "alpha")}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	resident := e.Export()
	if len(resident) != 2 {
		t.Fatalf("Export = %d segments, want 2", len(resident))
	}
	first, second := resident[0].ID, resident[1].ID

	// Populated by one segment.
	if got := e.BucketConstituents(3, constituentsBucketCount); len(got) != 1 || got[0] != first {
		t.Fatalf("BucketConstituents(3) = %v, want [%s]", got, first)
	}

	// Empty bucket.
	if got := e.BucketConstituents(5, constituentsBucketCount); got != nil {
		t.Fatalf("BucketConstituents(5) = %v, want nil for a bucket no segment holds", got)
	}

	// Split across both segments, returned in the set's own order and carrying each
	// segment ONCE even though the first holds two of the bucket's members.
	got := e.BucketConstituents(1, constituentsBucketCount)
	if len(got) != 2 || got[0] != first || got[1] != second {
		t.Fatalf("BucketConstituents(1) = %v, want [%s %s] — each holding segment exactly once", got, first, second)
	}
}
