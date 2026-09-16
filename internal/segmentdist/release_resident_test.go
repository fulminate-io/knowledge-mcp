// SPDX-License-Identifier: Apache-2.0

// release_resident_test.go covers the SEAL path's mapping republication: the
// durability write is what makes the release possible, so the release happens
// there, and every failure arm of it converges on the machinery the merge path
// already uses.

package segmentdist

import (
	"bytes"
	"log/slog"
	"runtime"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/bm25"
)

// sealedPoolFixture is a pool holding ONE segment SEALED BY THIS ENGINE and not
// yet persisted: the state a drain leaves behind, and the one whose payload is the
// encoder's output on the Go heap.
//
// It deliberately does NOT pre-Put the blob the way remapFixture does — the point
// of these cases is that persistResident is what makes the bytes durable and
// therefore what makes the release possible.
func sealedPoolFixture(t *testing.T) (*distManager[mockQuery, mockStats], *instrumentedCache) {
	t.Helper()
	ic := newInstrumentedCache(newDiskSegmentCache(t.TempDir(), 0, adviceRandom))
	dm := newDistManager[mockQuery, mockStats](
		newMockEngine(t), ic, graphSelector(kgtypes.GraphCode, "release"), "")

	require.NoError(t, dm.engine.Add([]searchengine.Document{doc("a", "alpha"), doc("b", "beta")}))
	require.NoError(t, dm.engine.Flush())
	require.Len(t, dm.engine.Export(), 1, "fixture control: the seal must have published one segment")
	return dm, ic
}

// searchHits is the result-equivalence observable: the ids a term matches, in the
// engine's own order.
func searchHits(e *searchengine.SegmentedIndex[mockQuery, mockStats], term string) []searchengine.ExternalID {
	var ids []searchengine.ExternalID
	for _, h := range e.Search(mockQuery{term: term}, 10) {
		ids = append(ids, h.ID)
	}
	return ids
}

// TestPersistResidentReleasesTheSealedBlob is requirement 1's row in the accounting
// the residency budget actually reads: after the durability write, the pool's
// modeled heap falls by at least the encoder output the sealed payload was holding,
// with the resident segment count unchanged.
//
// IT IS WRITTEN AGAINST residentBytes RATHER THAN THE PROVENANCE FIELD on purpose:
// this is the quantity the evictor compares, so a release that happened without the
// budget seeing it would still be a defect, and this row would still be red.
func TestPersistResidentReleasesTheSealedBlob(t *testing.T) {
	dm, _ := sealedPoolFixture(t)

	blobs := dm.engine.Export()
	blobBytes := int64(len(blobs[0].Bytes))
	require.Positive(t, blobBytes, "fixture control: the sealed segment must carry bytes")

	before := dm.residentBytes()
	countBefore := dm.engine.ResidentSegmentCount()
	hitsBefore := searchHits(dm.engine, "alpha")
	require.Len(t, hitsBefore, 1, "fixture control: the fixture must be searchable before the release")

	wrote, err := dm.persistResident()
	require.NoError(t, err)
	require.Equal(t, 1, wrote, "the durability write must have written the sealed blob")

	after := dm.residentBytes()
	require.GreaterOrEqual(t, before-after, blobBytes,
		"the pool's modeled heap must fall by at least the retained encoder output the release gave up")
	require.Equal(t, countBefore, dm.engine.ResidentSegmentCount(),
		"the release must not change what is resident — only where its bytes live")
	require.Equal(t, hitsBefore, searchHits(dm.engine, "alpha"),
		"results must be identical across the provenance swap")
}

// TestSealedPayloadsAreMappingBackedAfterPersist is the provenance half of the same
// property, read from the engine's own list rather than inferred from a number.
//
// AND IT RUNS A GC BEFORE READING BACK. The release hands the mapping's lifetime to
// a cleanup keyed on the new entry's reachability, so a payload whose mapping was
// released while the entry still held it would read unmapped memory here rather
// than at some later unrelated moment.
func TestSealedPayloadsAreMappingBackedAfterPersist(t *testing.T) {
	dm, _ := sealedPoolFixture(t)

	require.Len(t, dm.engine.HeapBackedResidentIDs(), 1,
		"control: a sealed, unpersisted segment must be heap-backed, or the release below proves nothing")

	_, err := dm.persistResident()
	require.NoError(t, err)

	require.Empty(t, dm.engine.HeapBackedResidentIDs(),
		"every resident payload must read its bytes from the mapping after the durability write")

	runtime.GC()
	runtime.GC()
	require.Equal(t, []searchengine.ExternalID{"a"}, searchHits(dm.engine, "alpha"),
		"the remapped payload must still be readable after a GC — the mapping is pinned by the entry")
	require.Equal(t, []searchengine.ExternalID{"b"}, searchHits(dm.engine, "beta"))
}

// TestReleaseIsIdempotentOverAlreadyMappedSegments covers the input class a
// re-persist meets: every resident blob is already in L2 and every payload is
// already a mapping, so the release must do nothing at all rather than re-map the
// whole pool on every drain.
func TestReleaseIsIdempotentOverAlreadyMappedSegments(t *testing.T) {
	dm, ic := sealedPoolFixture(t)

	_, err := dm.persistResident()
	require.NoError(t, err)
	id := dm.engine.Export()[0].ID
	mapsAfterFirst := countOps(ic, "getmapped", id)
	require.Positive(t, mapsAfterFirst, "control: the first persist must have mapped the blob")

	wrote, err := dm.persistResident()
	require.NoError(t, err)
	require.Equal(t, 0, wrote, "the second persist must write nothing: the blob is already durable")
	require.Equal(t, mapsAfterFirst, countOps(ic, "getmapped", id),
		"a pool that is already mapping-backed must not be re-mapped on every durability pass")
}

// TestReleaseFailureIsPendingAndConvergesOnNextTouch is requirement 1's error-arm
// row for the new caller. The payload stays CORRECT and heap-backed, the id is
// remembered, and the next consumer touch repairs it — the same four terminal
// conditions drainRemapPending documents, reached from the seal path.
func TestReleaseFailureIsPendingAndConvergesOnNextTouch(t *testing.T) {
	dm, ic := sealedPoolFixture(t)

	ic.mu.Lock()
	ic.failMapping = true
	ic.mu.Unlock()

	_, err := dm.persistResident()
	require.NoError(t, err,
		"a mapping failure must not fail the DURABILITY path: the bytes were written")

	id := dm.engine.Export()[0].ID
	require.Equal(t, []searchengine.SegmentID{id}, dm.pendingRemapIDs(),
		"a failed release must be RECORDED as pending, not logged and forgotten")
	require.Len(t, dm.engine.HeapBackedResidentIDs(), 1,
		"the payload must stay heap-backed and correct when its release fails")
	require.Equal(t, []searchengine.ExternalID{"a"}, searchHits(dm.engine, "alpha"),
		"a failed release must leave the segment searchable")

	ic.mu.Lock()
	ic.failMapping = false
	ic.mu.Unlock()

	require.NoError(t, dm.drainRemapPending())
	require.Empty(t, dm.pendingRemapIDs(), "the next consumer touch must converge")
	require.Empty(t, dm.engine.HeapBackedResidentIDs(),
		"the repaired segment must now read its bytes from the mapping")
	require.Equal(t, []searchengine.ExternalID{"a"}, searchHits(dm.engine, "alpha"))
}

// TestReleaseOfAnUncachedBlobIsPending covers the second cause a release can fail
// for — the blob is not in L2 at all — which is what a swallowed write error or a
// cache eviction in the window leaves behind. blockPut models it exactly: the write
// reports success and the bytes never land.
func TestReleaseOfAnUncachedBlobIsPending(t *testing.T) {
	dm, ic := sealedPoolFixture(t)

	ic.mu.Lock()
	ic.blockPut = true
	ic.mu.Unlock()

	_, err := dm.persistResident()
	require.NoError(t, err)

	id := dm.engine.Export()[0].ID
	require.Equal(t, []searchengine.SegmentID{id}, dm.pendingRemapIDs(),
		"a blob that never reached L2 must leave its release pending")
	require.Len(t, dm.engine.HeapBackedResidentIDs(), 1,
		"the payload stays heap-backed when there is nothing to map it over")
	require.Equal(t, []searchengine.ExternalID{"b"}, searchHits(dm.engine, "beta"),
		"correctness is unaffected by a failed release")
}

// TestReleaseDeclinesForASegmentThatLeftTheSet is the third arm: the segment is no
// longer resident by the time the remap reaches it. RemapResident declines and
// releases the mapping rather than resurrecting the entry, and the drain treats
// that as a terminus rather than a failure.
func TestReleaseDeclinesForASegmentThatLeftTheSet(t *testing.T) {
	dm, _ := sealedPoolFixture(t)
	id := dm.engine.Export()[0].ID

	// Persist first so the blob is durable, then unload the segment and drive the
	// remap directly — the window in which a superseding swap lands.
	_, err := dm.persistResident()
	require.NoError(t, err)
	dm.engine.Unload([]searchengine.SegmentID{id})

	cause, err := dm.remapOnce(id)
	require.NoError(t, err)
	require.Empty(t, cause, "a segment that is no longer resident is a terminus, not a failure")
	require.Empty(t, dm.pendingRemapIDs())
	require.Zero(t, dm.engine.ResidentSegmentCount())
}

// TestHeapBackedPoolOutweighsAMappedOne is requirement 2's guard row for the budget
// pass: the candidate ordering must see a heap-backed pool as the larger one. It is
// the mutation detector for the retained-blob term — remove the term and the two
// pools model identically, which is the blindness the ticket exists to remove.
func TestHeapBackedPoolOutweighsAMappedOne(t *testing.T) {
	heapBacked, _ := sealedPoolFixture(t)
	mapped, _ := sealedPoolFixture(t)

	_, err := mapped.persistResident()
	require.NoError(t, err)

	require.Empty(t, mapped.engine.HeapBackedResidentIDs(),
		"control: the mapped pool must really be mapping-backed")
	require.Len(t, heapBacked.engine.HeapBackedResidentIDs(), 1,
		"control: the heap-backed pool must really be heap-backed")
	require.Equal(t, heapBacked.engine.ResidentDocCount(), mapped.engine.ResidentDocCount(),
		"control: the two pools hold the same corpus, so the only difference is where the bytes live")

	require.Greater(t, heapBacked.residentBytes(), mapped.residentBytes(),
		"the budget must see a pool retaining its encoder output as the larger candidate")
}

// TestReleasedConstituentsStayMappedAcrossAGroupSwap is requirement 3's
// constituents row. No engine change makes it pass: the group's resolved
// constituents are whatever the seal path left behind, so if the release is not
// reaching a drain's sealed tails this row goes red while every other one stays
// green.
func TestReleasedConstituentsStayMappedAcrossAGroupSwap(t *testing.T) {
	dm, _ := sealedPoolFixture(t)
	_, err := dm.persistResident()
	require.NoError(t, err)
	require.Empty(t, dm.engine.HeapBackedResidentIDs(), "control: the constituents are mapped before the swap")

	// THE FRESH DOCUMENT GOES IN ITS OWN PARTITION, computed rather than assumed:
	// the harvest's accept predicate keeps only the members that belong to the
	// partition being built, so a document placed in the wrong one is silently
	// dropped and the row would assert nothing about the swap.
	const buckets = 2
	fresh := doc("c", "gamma")
	freshBucket := searchengine.BucketOf(fresh.ID, buckets)
	work := make([]searchengine.BucketWork, 0, buckets)
	for b := range buckets {
		w := searchengine.BucketWork{Bucket: b}
		if b == freshBucket {
			w.Docs = []searchengine.Document{fresh}
		}
		work = append(work, w)
	}

	constituents := []searchengine.SegmentID{dm.engine.Export()[0].ID}
	published, _, err := dm.engine.ReplaceBucketGroup(t.Context(), buckets, constituents, work)
	require.NoError(t, err)
	require.NotEmpty(t, published, "fixture control: the group swap must have published something")

	require.Empty(t, dm.engine.HeapBackedResidentIDs(),
		"a group swap over mapped constituents must leave nothing heap-backed: its outputs are mappings too")
	require.Equal(t, []searchengine.ExternalID{"c"}, searchHits(dm.engine, "gamma"),
		"the swap's own documents must be searchable")
}

// TestAFailedReleaseDoesNotPinTheEntryItFailedOn is the row for what a pending
// attempt may retain.
//
// WHAT THE PIN COST, stated precisely because the two retentions are different: an
// exported blob carries a reference to the resident ENTRY it came from, so a
// pending attempt that kept it held that entry — its membership index, its
// liveness bitset and its payload struct — reachable even after eviction dropped
// it from the segment set, which is the inverse of what the release is for. The
// attempt's BYTES are retained deliberately and stay: they are the input to the
// bound's one additive re-Put, and that retention is bounded by the pending cap and
// by remapMaxAttempts.
func TestAFailedReleaseDoesNotPinTheEntryItFailedOn(t *testing.T) {
	dm, ic := sealedPoolFixture(t)

	ic.mu.Lock()
	ic.failMapping = true
	ic.mu.Unlock()
	_, err := dm.persistResident()
	require.NoError(t, err)

	id := dm.engine.Export()[0].ID
	dm.resMu.Lock()
	attempt, pending := dm.remapPending[id]
	dm.resMu.Unlock()
	require.True(t, pending, "control: the failed release must have left something pending")

	require.False(t, attempt.blob.PinsMapping(),
		"a pending seal-path attempt must not hold the resident entry alive: the entry is what eviction reclaims")
	require.NotNil(t, attempt.blob.Bytes,
		"dropping the pin must not drop the bytes: they are what the bound's one additive re-Put writes")

	// KNOWN-POSITIVE: the merge path's attempt, whose payload IS a mapping owned by
	// an entry, still carries its pin — the two callers differ in what they may
	// forget, and a blanket drop would reintroduce the use-after-unmap that pin
	// exists for.
	merged := dm.engine.Export()[0]
	require.True(t, merged.PinsMapping(),
		"control: an exported blob does carry the pin, so the assertion above is about what this caller kept")
}

// TestReleaseCountsDeclinesSeparatelyFromReleases pins the operator-facing line
// this change's evidence is read from: `released=N` must mean N payloads were
// SWAPPED, not N ids that reached the batch and may have left the set on the way.
func TestReleaseCountsDeclinesSeparatelyFromReleases(t *testing.T) {
	dm, _ := sealedPoolFixture(t)

	var logBuf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	_, err := dm.persistResident()
	require.NoError(t, err)

	require.Contains(t, logBuf.String(), "released=1", "the one segment was swapped")
	require.Contains(t, logBuf.String(), "declined=0", "nothing left the set during this pass")
	require.Contains(t, logBuf.String(), "failed=0")
	require.Empty(t, dm.engine.HeapBackedResidentIDs(),
		"control: the count must agree with the engine — nothing heap-backed is left")
}

// TestRealBM25PoolSurvivesTheReleaseUnderGC is the same release-before-attach
// property as the engine's own rows, driven through the REAL bm25 format and the
// real disk cache.
//
// IT EXISTS BECAUSE THE DOUBLE CANNOT SEE THE DEFECT. This package's mock format
// decodes by COPYING its input, so a payload whose mapping was released early
// keeps answering correctly and every assertion passes. A bm25 mapped segment
// reads its postings, dictionaries and member table IN PLACE over the mapped
// bytes, so the same mistake faults the process on the next search — which is
// what this row makes reachable, deliberately, in a package that otherwise only
// searches doubles.
func TestRealBM25PoolSurvivesTheReleaseUnderGC(t *testing.T) {
	engine := searchengine.New[bm25.Query, *bm25.CorpusStats](bm25.New(), searchengine.Options{
		MinSegmentDocs:     1 << 20,
		DeletesPctAllowed:  searchengine.MergeDisabledDeadRatio,
		SegmentCountTarget: searchengine.MergeDisabledCountTarget,
		ScratchDir:         t.TempDir(),
	})
	t.Cleanup(engine.Close)
	cache := newDiskSegmentCache(t.TempDir(), 0, adviceRandom)
	dm := newDistManager[bm25.Query, *bm25.CorpusStats](
		engine, cache, graphSelector(kgtypes.GraphCode, "release-bm25"), bm25.New().Name())

	docs := make([]searchengine.Document, 0, 64)
	for i := range 64 {
		docs = append(docs, searchengine.Document{
			ID: "node-" + strconv.Itoa(i),
			Fields: map[string]string{
				searchengine.FieldContent:    "alpha beta gamma delta epsilon " + strconv.Itoa(i),
				searchengine.FieldSymbolName: "Symbol" + strconv.Itoa(i),
			},
		})
	}
	require.NoError(t, engine.Add(docs))
	require.NoError(t, engine.Flush())
	require.Len(t, engine.HeapBackedResidentIDs(), 1,
		"fixture control: the sealed bm25 segment must be heap-backed before the release")

	before := len(engine.Search(bm25.NewQuery("alpha"), 10))
	require.Positive(t, before, "fixture control: the corpus must answer before the release")

	_, err := dm.persistResident()
	require.NoError(t, err)
	require.Empty(t, engine.HeapBackedResidentIDs(),
		"the payload must read the mapping after the durability write")

	// THE FAULT WINDOW: if the release had been handed over rather than attached to
	// the winning entry, these collections unmap bytes the resident payload reads in
	// place, and the search below faults instead of failing.
	runtime.GC()
	runtime.GC()
	require.Len(t, engine.Search(bm25.NewQuery("alpha"), 10), before,
		"the mapped payload must still answer identically after a collection")
	require.Len(t, engine.Search(bm25.NewQuery("Symbol7"), 10), 1,
		"a term from the name field must still resolve through the mapped dictionary")
}
