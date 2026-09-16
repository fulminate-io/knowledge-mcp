package searchengine

import (
	"errors"
	"strings"
	"testing"
)

// layer_release_test.go gates BuildLayerReleasing's per-partition persist hook at the
// four outcomes a hook can produce — a mapping, a decline, a stored copy that will not
// decode, and a durability failure — plus the no-hook case that must stay exactly
// BuildLayer.
//
// WHY THE HOOK EXISTS AT ALL, since a reader meeting it here will ask: BuildLayer
// accumulates every partition's entry AND its encoded payload before it returns, so a
// layer of N partitions is N encoded segments on the heap at the instant a rebuild is
// at its largest. No call placed after the build can move that. The hook is the only
// seam at which a partition's encoder output becomes unreachable while its successors
// are still being built.

// layerReleaseDocs builds n documents for one partition, ids salted by tag so two
// partitions never collide.
func layerReleaseDocs(t *testing.T, n int, tag string) []Document {
	t.Helper()
	docs := make([]Document, 0, n)
	for i := range n {
		docs = append(docs, doc(tag+"-"+string(rune('a'+i)), "alpha "+tag))
	}
	return docs
}

// twoPartitionWork is the smallest layer whose release is per-partition rather than
// per-layer: one partition's release has nothing to be "before".
func twoPartitionWork(t *testing.T) []BucketWork {
	t.Helper()
	return []BucketWork{
		{Bucket: 0, Docs: layerReleaseDocs(t, 3, "p0")},
		{Bucket: 1, Docs: layerReleaseDocs(t, 3, "p1")},
	}
}

// mappingOf turns a built partition's blob into what a persist hook returns: the same
// bytes back, carrying a Release that records having been called. The engine cannot
// tell this from a real mmap of the stored file, which is the point — the bytes a
// content-addressed cache hands back ARE these bytes.
func mappingOf(blob SegmentBlob, released *int) SegmentBlob {
	return SegmentBlob{
		ID:       blob.ID,
		Bytes:    blob.Bytes,
		Envelope: blob.Envelope,
		Release:  func() { *released++ },
	}
}

// TestBuildLayerReleasingSwapsEachPartitionForItsMapping is the primary row: with a
// hook that maps every partition, the published layer holds NO heap-backed payload and
// answers exactly as the same layer built without a hook does.
func TestBuildLayerReleasingSwapsEachPartitionForItsMapping(t *testing.T) {
	e := layerEngine(t, mockFormat{}, nil)
	defer e.Close()

	seen := 0
	built, err := e.BuildLayerReleasing(twoPartitionWork(t), func(b SegmentBlob) (SegmentBlob, bool, error) {
		seen++
		return mappingOf(b, new(int)), true, nil
	})
	if err != nil {
		t.Fatalf("BuildLayerReleasing: %v", err)
	}
	if seen != 2 {
		t.Fatalf("the hook was called %d times, want one call per partition (2)", seen)
	}
	if ids, rerr := built.Unreleased(); len(ids) != 0 || rerr != nil {
		t.Fatalf("Unreleased = (%v, %v), want nothing owed when every partition mapped", ids, rerr)
	}

	if _, _, err := e.ReplaceLayer(built); err != nil {
		t.Fatalf("ReplaceLayer: %v", err)
	}
	if ids := e.HeapBackedResidentIDs(); len(ids) != 0 {
		t.Fatalf("HeapBackedResidentIDs = %v after a released build, want none", ids)
	}
	if got := e.ResidentSegmentCount(); got != 2 {
		t.Fatalf("ResidentSegmentCount = %d, want the 2 partitions supplied", got)
	}
	if got := e.ResidentDocCount(); got != 6 {
		t.Fatalf("ResidentDocCount = %d, want the 6 documents supplied", got)
	}
	if hits := e.Search(mockQuery{term: "alpha"}, 10); len(hits) != 6 {
		t.Fatalf("the released layer answered %d hits, want all 6 documents", len(hits))
	}
}

// TestBuildLayerWithNoHookLeavesEveryPartitionHeapBacked is the CONTROL for the row
// above and the compatibility row for the nil-hook case: BuildLayer must still be
// exactly the build it was, with every payload the encoder's own output.
//
// Without it the primary row proves nothing — an engine that never held its encoder
// output would satisfy it while the release did nothing at all.
func TestBuildLayerWithNoHookLeavesEveryPartitionHeapBacked(t *testing.T) {
	e := layerEngine(t, mockFormat{}, nil)
	defer e.Close()

	built, err := e.BuildLayer(twoPartitionWork(t))
	if err != nil {
		t.Fatalf("BuildLayer: %v", err)
	}
	if ids, rerr := built.Unreleased(); len(ids) != 0 || rerr != nil {
		t.Fatalf("Unreleased = (%v, %v), want nothing owed when no release was asked for", ids, rerr)
	}
	if _, _, err := e.ReplaceLayer(built); err != nil {
		t.Fatalf("ReplaceLayer: %v", err)
	}
	if ids := e.HeapBackedResidentIDs(); len(ids) != 2 {
		t.Fatalf("HeapBackedResidentIDs = %v, want both partitions heap-backed with no hook", ids)
	}
}

// TestBuildLayerReleasingKeepsAPartitionTheHookDeclines: a hook that cannot map a
// partition — the blob is not cached, the mmap is unavailable — leaves that partition
// CORRECT and heap-backed and names it, and the rebuild proceeds.
//
// A DECLINE IS NOT A FAILURE, and that disposition is the whole reason the memory
// property cannot be allowed to fail a rebuild: the partition is durable and serves the
// right documents, and only the place its bytes live was not improved.
func TestBuildLayerReleasingKeepsAPartitionTheHookDeclines(t *testing.T) {
	e := layerEngine(t, mockFormat{}, nil)
	defer e.Close()

	first := true
	built, err := e.BuildLayerReleasing(twoPartitionWork(t), func(b SegmentBlob) (SegmentBlob, bool, error) {
		if first {
			first = false
			return SegmentBlob{}, false, nil
		}
		return mappingOf(b, new(int)), true, nil
	})
	if err != nil {
		t.Fatalf("BuildLayerReleasing: %v", err)
	}
	ids, rerr := built.Unreleased()
	if len(ids) != 1 {
		t.Fatalf("Unreleased ids = %v, want exactly the one partition the hook declined", ids)
	}
	if rerr != nil {
		t.Fatalf("Unreleased err = %v, want nil: a decline is not an error", rerr)
	}
	if _, _, err := e.ReplaceLayer(built); err != nil {
		t.Fatalf("ReplaceLayer: %v", err)
	}
	if heap := e.HeapBackedResidentIDs(); len(heap) != 1 || heap[0] != ids[0] {
		t.Fatalf("HeapBackedResidentIDs = %v, want exactly the declined partition %v", heap, ids)
	}
	if hits := e.Search(mockQuery{term: "alpha"}, 10); len(hits) != 6 {
		t.Fatalf("a declined release must leave every document searchable; got %d hits, want 6", len(hits))
	}
}

// TestBuildLayerReleasingFreesAMappingTheHookDeclinedWith is the ok=false arm's half of
// the ownership rule, on the input class the in-tree hook does not produce.
//
// OWNERSHIP PASSES ON RETURN, not on ok. LayerPartitionPersist's contract is that a
// Release handed back is attached to the entry that serves it OR freed here, on the
// same terms RemapResidentBatch takes a blob — and RemapResidentBatch frees a DECLINED
// blob for exactly this reason. A hook that says "I mapped it and decided not to offer
// it" — a cache whose blob was evicted between the write and the map, a mapping that
// arrived too small to be worth swapping — is an ordinary thing for a hook to do, and
// dropping its Release leaks the mapping with no error, no log and no test failure:
// the address space is simply never returned.
//
// THE COUNT IS EXACTLY ONE, not "at least one": a double free is the other half of the
// rule, and an arm that both released here and attached the same mapping would satisfy
// a >= 1 assertion while unmapping memory a later reader still holds.
func TestBuildLayerReleasingFreesAMappingTheHookDeclinedWith(t *testing.T) {
	e := layerEngine(t, mockFormat{}, nil)
	defer e.Close()

	freed := 0
	var declinedID SegmentID
	built, err := e.BuildLayerReleasing(twoPartitionWork(t), func(b SegmentBlob) (SegmentBlob, bool, error) {
		if declinedID == "" {
			declinedID = b.ID
			// A NON-ZERO blob carrying a live Release, which is what the in-tree hook
			// never returns on this arm and what the contract nonetheless admits.
			return mappingOf(b, &freed), false, nil
		}
		return mappingOf(b, new(int)), true, nil
	})
	if err != nil {
		t.Fatalf("BuildLayerReleasing: %v", err)
	}
	if freed != 1 {
		t.Fatalf("the declined mapping was released %d times, want exactly 1 — ownership passes to the "+
			"engine on return, so a Release the hook hands back with ok=false is freed here or leaked", freed)
	}

	ids, rerr := built.Unreleased()
	if len(ids) != 1 || ids[0] != declinedID {
		t.Fatalf("Unreleased ids = %v, want exactly the declined partition (%s)", ids, declinedID)
	}
	if rerr != nil {
		t.Fatalf("Unreleased err = %v, want nil: a decline is not an error however it was spelled", rerr)
	}

	if _, _, err := e.ReplaceLayer(built); err != nil {
		t.Fatalf("ReplaceLayer: %v", err)
	}
	if heap := e.HeapBackedResidentIDs(); len(heap) != 1 || heap[0] != declinedID {
		t.Fatalf("HeapBackedResidentIDs = %v, want the declined partition kept on its CORRECT heap payload (%s)",
			heap, declinedID)
	}
	if hits := e.Search(mockQuery{term: "alpha"}, 10); len(hits) != 6 {
		t.Fatalf("every document must still be searchable; got %d hits, want 6", len(hits))
	}
}

// TestBuildLayerReleasingKeepsAPartitionWhoseStoredCopyWillNotDecode is the arm a
// manager-level test cannot reach on demand: the hook reports success and hands back
// bytes the format refuses.
//
// THREE THINGS MUST HOLD AT ONCE, and each has its own way of going wrong silently.
// The partition keeps its CORRECT heap payload (it must not be published broken). The
// mapping is RELEASED here (nothing will ever attach it, and a mapping nobody frees and
// nobody references is a leak with no error and no log). And the decode failure is
// REPORTED rather than swallowed, because a stored copy that will not decode is a fact
// about the file on disk that an operator needs.
func TestBuildLayerReleasingKeepsAPartitionWhoseStoredCopyWillNotDecode(t *testing.T) {
	e := layerEngine(t, mockFormat{}, nil)
	defer e.Close()

	freed := 0
	var badID SegmentID
	built, err := e.BuildLayerReleasing(twoPartitionWork(t), func(b SegmentBlob) (SegmentBlob, bool, error) {
		if badID == "" {
			badID = b.ID
			bad := mappingOf(b, &freed)
			bad.Bytes = []byte("not a segment")
			return bad, true, nil
		}
		return mappingOf(b, new(int)), true, nil
	})
	if err != nil {
		t.Fatalf("BuildLayerReleasing: %v — an undecodable stored copy must not fail the build", err)
	}

	ids, rerr := built.Unreleased()
	if len(ids) != 1 || ids[0] != badID {
		t.Fatalf("Unreleased ids = %v, want exactly the partition whose bytes would not decode (%s)", ids, badID)
	}
	if rerr == nil {
		t.Fatal("Unreleased err = nil, want the decode failure reported rather than swallowed")
	}
	if !strings.Contains(rerr.Error(), badID) {
		t.Fatalf("the reported error does not name the partition it is about: %v", rerr)
	}
	if freed != 1 {
		t.Fatalf("the undecodable mapping was released %d times, want exactly 1 — "+
			"no entry can ever attach it, so leaving it unreleased leaks it silently", freed)
	}

	if _, _, err := e.ReplaceLayer(built); err != nil {
		t.Fatalf("ReplaceLayer: %v", err)
	}
	if heap := e.HeapBackedResidentIDs(); len(heap) != 1 || heap[0] != badID {
		t.Fatalf("HeapBackedResidentIDs = %v, want exactly the undecodable partition kept on its CORRECT "+
			"heap payload (%s)", heap, badID)
	}
	if hits := e.Search(mockQuery{term: "alpha"}, 10); len(hits) != 6 {
		t.Fatalf("every document must still be searchable; got %d hits, want 6", len(hits))
	}
}

// TestBuildLayerReleasingAbortsOnAPersistError is the DURABILITY arm, and the one
// outcome that must stop the build. The hook's error means the partition's bytes are
// not on disk, and a layer that reached the swap in that state would leave the engine
// serving blobs that exist only in memory — the exact window the write-before-swap
// ordering exists to close.
func TestBuildLayerReleasingAbortsOnAPersistError(t *testing.T) {
	e := layerEngine(t, mockFormat{}, nil)
	defer e.Close()

	injected := errors.New("injected L2 write failure")
	built, err := e.BuildLayerReleasing(twoPartitionWork(t), func(SegmentBlob) (SegmentBlob, bool, error) {
		return SegmentBlob{}, false, injected
	})
	if !errors.Is(err, injected) {
		t.Fatalf("BuildLayerReleasing err = %v, want the hook's own error wrapped", err)
	}
	if built != nil {
		t.Fatalf("a failed build returned a handle (%d partitions) — a partial layer must never be publishable",
			built.Len())
	}
	if got := e.ResidentSegmentCount(); got != 0 {
		t.Fatalf("ResidentSegmentCount = %d after a failed build, want the engine untouched", got)
	}
}

// TestBuildLayerReleasingBlobsCarryTheMappedBytes pins what the caller ships after a
// release: Blobs() must hand back the MAPPED bytes for a released partition, not the
// encoder output it gave up.
//
// IT IS NOT A DETAIL. A caller holding the old bytes would keep the encoder output
// reachable from the blob slice, so the layer's heap would be given up by the entries
// and held by the blobs — the release would show as done and change nothing.
func TestBuildLayerReleasingBlobsCarryTheMappedBytes(t *testing.T) {
	e := layerEngine(t, mockFormat{}, nil)
	defer e.Close()

	mapped := map[SegmentID][]byte{}
	built, err := e.BuildLayerReleasing(twoPartitionWork(t), func(b SegmentBlob) (SegmentBlob, bool, error) {
		out := mappingOf(b, new(int))
		// A DISTINCT BACKING ARRAY carrying the same bytes, so "the blob points at the
		// hook's slice" is observable rather than trivially true of the built one.
		out.Bytes = append([]byte(nil), b.Bytes...)
		mapped[b.ID] = out.Bytes
		return out, true, nil
	})
	if err != nil {
		t.Fatalf("BuildLayerReleasing: %v", err)
	}
	// PER-BLOB FAILURES REPORT AND KEEP GOING, so one run names every partition whose
	// blob still points at the encoder output rather than stopping at the first.
	for _, b := range built.Blobs() {
		want, ok := mapped[b.ID]
		if !ok {
			t.Errorf("blob %s was never offered to the hook", b.ID)
			continue
		}
		if &b.Bytes[0] != &want[0] {
			t.Errorf("blob %s carries bytes that are not the mapping's — the encoder output is still reachable "+
				"through the layer's blob slice", b.ID)
		}
	}
}
