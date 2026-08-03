package searchengine

import (
	"fmt"
	"testing"
)

// layer_swap_test.go gates BuildLayer/ReplaceLayer at the four edges that decide
// whether a from-scratch reset is safe: it replaces the WHOLE layer, it touches
// nothing when the build fails, it never retires an id it just republished, and it
// preserves whatever another writer published while it was building.

// buildFailFormat wraps mockFormat so a test can FAIL a chosen Build call. The
// failure has to be injectable at Build specifically — the group swap's gateFormat
// fails Merge, and a from-scratch layer never merges.
type buildFailFormat struct {
	mockFormat
	calls  int
	failAt int // 1-based Build call to fail; 0 disables
}

func (b *buildFailFormat) Build(docs []Document) (Segment[mockQuery, mockStats], error) {
	b.calls++
	if b.failAt == b.calls {
		return nil, fmt.Errorf("injected build failure on call %d", b.calls)
	}
	return b.mockFormat.Build(docs)
}

// layerEngine builds a mock engine over a caller-supplied format, mirroring
// bucketTestEngine's options so segment layout stays the test's to own.
func layerEngine(f SegmentFormat[mockQuery, mockStats], onMerge OnMergeFunc) *SegmentedIndex[mockQuery, mockStats] {
	return New[mockQuery, mockStats](f, Options{
		MinSegmentDocs:     1,
		DeletesPctAllowed:  MergeDisabledDeadRatio,
		SegmentCountTarget: MergeDisabledCountTarget,
		OnMerge:            onMerge,
	})
}

// sealOne adds docs and force-seals them into their own segment, returning its id.
func sealOne(t *testing.T, e *SegmentedIndex[mockQuery, mockStats], docs []Document) SegmentID {
	t.Helper()
	before := map[SegmentID]bool{}
	for _, b := range e.Export() {
		before[b.ID] = true
	}
	if err := e.Add(docs); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := e.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	for _, b := range e.Export() {
		if !before[b.ID] {
			return b.ID
		}
	}
	t.Fatalf("no new segment appeared after sealing %d docs", len(docs))
	return ""
}

// residentIDSet is the engine's current published id set.
func residentIDSet(e *SegmentedIndex[mockQuery, mockStats]) map[SegmentID]bool {
	out := map[SegmentID]bool{}
	for _, b := range e.Export() {
		out[b.ID] = true
	}
	return out
}

// TestReplaceLayerSwapsWholeLayerFromScratch is the CORE property: after the swap the
// resident set is EXACTLY the built partitions, and nothing the caller did not supply
// survives.
//
// This is what neither existing swap primitive can do. Passing no constituents to the
// group swap removes nothing and leaves the new layer sitting BESIDE the old one —
// which is the accumulation defect verbatim — while passing every resident id harvests
// their members forward into the outputs, which destroys the from-scratch property a
// reset exists to provide. The assertion below is deliberately about what is ABSENT.
func TestReplaceLayerSwapsWholeLayerFromScratch(t *testing.T) {
	// FIXTURE CONSTANTS — never read back from the engine, so a defect that loses or
	// duplicates documents cannot define its own expectation.
	const priorDocs, newDocs = 6, 4

	e := layerEngine(mockFormat{}, nil)
	defer e.Close()

	taken := map[string]bool{}
	prior := make([]Document, 0, priorDocs)
	for range priorDocs {
		prior = append(prior, doc(idForBucket(t, 0, taken), "old"))
	}
	priorID := sealOne(t, e, prior)
	if got := e.ResidentDocCount(); got != priorDocs {
		t.Fatalf("ResidentDocCount = %d before the swap, want %d", got, priorDocs)
	}

	replacement := make([]Document, 0, newDocs)
	for range newDocs {
		replacement = append(replacement, doc(idForBucket(t, 1, taken), "new"))
	}

	built, err := e.BuildLayer([]BucketWork{{Bucket: 1, Docs: replacement}})
	if err != nil {
		t.Fatalf("BuildLayer: %v", err)
	}
	published, retired, err := e.ReplaceLayer(built)
	if err != nil {
		t.Fatalf("ReplaceLayer: %v", err)
	}

	if len(published) != 1 {
		t.Fatalf("published %d segments, want exactly the 1 partition supplied", len(published))
	}
	if len(retired) != 1 || retired[0] != priorID {
		t.Fatalf("retired = %v, want exactly the prior layer's %q", retired, priorID)
	}

	resident := residentIDSet(e)
	if len(resident) != 1 {
		t.Fatalf("resident holds %d segments after the swap, want exactly the 1 built", len(resident))
	}
	if resident[priorID] {
		t.Fatalf("the prior segment %q is STILL resident — the layer was appended beside the old one, not swapped", priorID)
	}
	if !resident[published[0]] {
		t.Fatalf("the built segment %q is not resident after the swap", published[0])
	}
	if got := e.ResidentDocCount(); got != newDocs {
		t.Fatalf("ResidentDocCount = %d after the swap, want exactly the %d supplied — "+
			"anything more means unsupplied members were carried forward", got, newDocs)
	}
}

// TestReplaceLayerBuildErrorLeavesOldLayerServing is the FAILURE catcher: a build that
// fails must leave the engine exactly as it found it.
//
// It matters more here than for a consolidating swap. Today a mid-rebuild failure
// leaves a partial layer memoized in a staging engine, where it is pinned and later
// published; build-aside is supposed to make that impossible by failing before the set
// is touched at all. The assertion is on the FULL id list in order, not on a count — a
// partial swap that happened to preserve the segment count would pass a count check.
func TestReplaceLayerBuildErrorLeavesOldLayerServing(t *testing.T) {
	const priorDocs = 5

	// Fail the SECOND build so the first partition succeeds first: a failure on call
	// one could pass by never having built anything at all.
	format := &buildFailFormat{failAt: 2}
	e := layerEngine(format, nil)
	defer e.Close()

	taken := map[string]bool{}
	prior := make([]Document, 0, priorDocs)
	for range priorDocs {
		prior = append(prior, doc(idForBucket(t, 0, taken), "old"))
	}
	sealOne(t, e, prior)

	before := e.Export()
	beforeCount := e.ResidentDocCount()
	if len(before) == 0 {
		t.Fatal("the fixture must have a prior layer to preserve")
	}

	first := []Document{doc(idForBucket(t, 1, taken), "new")}
	second := []Document{doc(idForBucket(t, 2, taken), "new")}
	built, err := e.BuildLayer([]BucketWork{{Bucket: 1, Docs: first}, {Bucket: 2, Docs: second}})
	if err == nil {
		t.Fatal("BuildLayer returned no error, but the format was told to fail its second Build")
	}
	if built != nil {
		t.Fatal("BuildLayer returned a handle alongside an error — a partial layer must never escape")
	}

	after := e.Export()
	if len(after) != len(before) {
		t.Fatalf("Export = %d segments after the failed build, want the %d it started with", len(after), len(before))
	}
	for i := range before {
		if after[i].ID != before[i].ID {
			t.Fatalf("segment %d changed from %q to %q across a FAILED build — the set was touched", i, before[i].ID, after[i].ID)
		}
	}
	if got := e.ResidentDocCount(); got != beforeCount {
		t.Fatalf("ResidentDocCount = %d after the failed build, want the %d it started with", got, beforeCount)
	}
}

// TestReplaceLayerSparesAnAliasedRepublishedID is the ALIAS catcher.
//
// A segment id is a content hash, so a partition rebuilt from the documents a resident
// segment already holds encodes to the same bytes and mints the SAME id. Retiring by
// name would then hand the owner the id of the segment this very call just published,
// and the owner would delete the stored copy of live data.
//
// THE ALIASING PRECONDITION IS ASSERTED FIRST. If the format ever stopped reproducing
// its bytes the two ids would differ, the hazard would not arise, and every assertion
// below would hold for the wrong reason.
func TestReplaceLayerSparesAnAliasedRepublishedID(t *testing.T) {
	const corpus = 4

	var fired []MergeResult
	e := layerEngine(mockFormat{}, func(res MergeResult) { fired = append(fired, res) })
	defer e.Close()

	taken := map[string]bool{}
	docs := make([]Document, 0, corpus)
	for range corpus {
		docs = append(docs, doc(idForBucket(t, 0, taken), "alpha"))
	}
	priorID := sealOne(t, e, docs)
	fired = nil

	// Rebuild the SAME documents as the whole layer: Build is deterministic over the
	// document list, so this reproduces the resident segment's bytes and its id.
	built, err := e.BuildLayer([]BucketWork{{Bucket: 0, Docs: docs}})
	if err != nil {
		t.Fatalf("BuildLayer: %v", err)
	}
	published, retired, err := e.ReplaceLayer(built)
	if err != nil {
		t.Fatalf("ReplaceLayer: %v", err)
	}

	if len(published) != 1 || published[0] != priorID {
		t.Fatalf("precondition lost: published %v != prior %q — the aliasing this test exists for no longer occurs",
			published, priorID)
	}
	for _, id := range retired {
		if id == priorID {
			t.Fatalf("the republished id %q was reported as RETIRED — the owner would reclaim the segment just published", id)
		}
	}
	for _, res := range fired {
		for _, id := range res.Removed {
			if id == priorID {
				t.Fatalf("the reclaim hook named the republished id %q inside Removed", id)
			}
		}
	}
	if got := e.ResidentDocCount(); got != corpus {
		t.Fatalf("ResidentDocCount = %d, want %d — the aliased segment was retired out from under the swap", got, corpus)
	}
}

// TestReplaceLayerPreservesAConcurrentlyAddedSegment is the CONTENTION catcher, and
// BOTH halves are asserted in one test because the destructive branch passes the first
// half silently.
//
// The removal set names only what was resident when the build BEGAN. A segment another
// writer publishes during the build is therefore not in it, is carried through by
// withReplacedGroup, and survives. Recomputing that set on a lost CAS race — the
// reading an earlier draft of this design took — would sweep the concurrent segment in,
// swap it straight back out, and let it be reaped, with no error raised anywhere. A
// test asserting only that the prior layer is GONE passes against that bug; a test
// asserting only that the concurrent segment SURVIVES passes against a swap that did
// nothing at all.
//
// The window is deterministic rather than raced: splitting build from swap makes the
// interval between the snapshot and the CAS an explicit region the test writes into.
func TestReplaceLayerPreservesAConcurrentlyAddedSegment(t *testing.T) {
	const priorDocs, concurrentDocs, newDocs = 5, 3, 4

	e := layerEngine(mockFormat{}, nil)
	defer e.Close()

	taken := map[string]bool{}
	prior := make([]Document, 0, priorDocs)
	for range priorDocs {
		prior = append(prior, doc(idForBucket(t, 0, taken), "old"))
	}
	priorID := sealOne(t, e, prior)

	// (1) The build captures its snapshot HERE — the prior layer only.
	replacement := make([]Document, 0, newDocs)
	for range newDocs {
		replacement = append(replacement, doc(idForBucket(t, 1, taken), "new"))
	}
	built, err := e.BuildLayer([]BucketWork{{Bucket: 1, Docs: replacement}})
	if err != nil {
		t.Fatalf("BuildLayer: %v", err)
	}

	// (2) INSIDE THE WINDOW: another writer publishes a segment, exactly as an embed
	// drain does while a rebuild is running.
	drained := make([]Document, 0, concurrentDocs)
	for range concurrentDocs {
		drained = append(drained, doc(idForBucket(t, 2, taken), "drain"))
	}
	concurrentID := sealOne(t, e, drained)

	// (3) The swap lands after it.
	published, retired, err := e.ReplaceLayer(built)
	if err != nil {
		t.Fatalf("ReplaceLayer: %v", err)
	}

	resident := residentIDSet(e)

	// HALF ONE — the prior layer is gone.
	if resident[priorID] {
		t.Fatalf("the prior segment %q survived the swap — the layer was not replaced", priorID)
	}
	foundRetired := false
	for _, id := range retired {
		if id == priorID {
			foundRetired = true
		}
		if id == concurrentID {
			t.Fatalf("the CONCURRENTLY published segment %q was reported retired — "+
				"the removal set was recomputed and swept in another writer's work", id)
		}
	}
	if !foundRetired {
		t.Fatalf("retired = %v, want it to name the prior layer's %q", retired, priorID)
	}

	// HALF TWO — the concurrent writer's segment survived and is still published.
	if !resident[concurrentID] {
		t.Fatalf("the segment published DURING the build (%q) is gone — a recomputed removal set "+
			"discarded a concurrent writer's work with no error raised", concurrentID)
	}
	if !resident[published[0]] {
		t.Fatalf("the built segment %q is not resident after the swap", published[0])
	}
	if got := e.ResidentDocCount(); got != newDocs+concurrentDocs {
		t.Fatalf("ResidentDocCount = %d, want %d (the built layer plus the concurrent segment, each exactly once)",
			got, newDocs+concurrentDocs)
	}
}

// TestReplaceLayerRefusesAForeignOrEmptyHandle pins the two structural refusals. Both
// are cheap guards on a DESTRUCTIVE primitive, and both describe a caller mistake that
// would otherwise be silent and unrecoverable: publishing one engine's layer into
// another replaces a corpus with an unrelated one, and publishing an empty layer
// replaces a corpus with nothing.
func TestReplaceLayerRefusesAForeignOrEmptyHandle(t *testing.T) {
	one := layerEngine(mockFormat{}, nil)
	defer one.Close()
	two := layerEngine(mockFormat{}, nil)
	defer two.Close()

	taken := map[string]bool{}
	sealOne(t, two, []Document{doc(idForBucket(t, 0, taken), "resident")})
	before := two.Export()

	built, err := one.BuildLayer([]BucketWork{{Bucket: 0, Docs: []Document{doc(idForBucket(t, 1, taken), "foreign")}}})
	if err != nil {
		t.Fatalf("BuildLayer: %v", err)
	}
	if _, _, err := two.ReplaceLayer(built); err == nil {
		t.Fatal("ReplaceLayer accepted a layer built by a DIFFERENT engine")
	}
	if after := two.Export(); len(after) != len(before) {
		t.Fatalf("the refused foreign handle still changed the resident set: %d segments, want %d", len(after), len(before))
	}

	// An empty layer — every work item had no documents — must be refused too.
	empty, err := one.BuildLayer([]BucketWork{{Bucket: 0}})
	if err != nil {
		t.Fatalf("BuildLayer over empty work: %v", err)
	}
	if empty.Len() != 0 {
		t.Fatalf("the empty-work handle holds %d partitions, want 0", empty.Len())
	}
	if _, _, err := one.ReplaceLayer(empty); err == nil {
		t.Fatal("ReplaceLayer accepted an EMPTY layer — that replaces the corpus with nothing")
	}

	if _, _, err := one.ReplaceLayer(nil); err == nil {
		t.Fatal("ReplaceLayer accepted a nil handle")
	}
}
