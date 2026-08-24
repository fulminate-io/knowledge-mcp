// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"testing"
	"unsafe"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/bm25"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/hnsw"
)

// TestRemapMergedRepublishesTheMapping proves the swap remapMerged exists to
// perform actually happens, FOR BOTH FORMATS.
//
// THE ASSERTION IS ON BACKING MEMORY, NOT ON BYTES. A heap copy and a mapping of
// the same file compare byte-equal, so a bytes assertion would pass on a remap
// that never happened — precisely the regression this seam is measured against.
// The test imports a HEAP copy, asserts the published payload is backed by THAT
// SLICE, remaps, and requires it to have moved off it.
//
// IT COMPARES AGAINST THE HEAP SLICE RATHER THAN AGAINST A MAPPING TAKEN HERE,
// and that is the only correct comparison rather than a weaker one: each
// GetMapped mmaps the file AGAIN at a fresh address, so a pointer taken from a
// second mapping can never equal the one the entry holds even when the
// republication is perfect. A first draft of this test asserted exactly that and
// failed against correct code on both formats.
//
// The pre-remap equality is the known-negative: without it, a build where
// imports were already mapped would report a swap it never made.
//
// Both formats are covered because reclaimMerged is wired through Options.OnMerge
// for both, so a republication that worked for only one would leave the other
// permanently heap-resident — the exact asymmetry the both-arm drain also guards.
func TestRemapMergedRepublishesTheMapping(t *testing.T) {
	docs := vecContentDocs(4)

	for _, tc := range []struct {
		name string
		blob func(*testing.T) searchengine.SegmentBlob
		mgr  func(*testing.T, segmentL2Cache) republishArm
	}{
		{
			name: "hnsw",
			blob: func(t *testing.T) searchengine.SegmentBlob { return consolidatedHNSWBlob(t, docs) },
			mgr: func(t *testing.T, c segmentL2Cache) republishArm {
				t.Helper()
				eng := closeOnCleanup(t, searchengine.New[[]byte, struct{}](hnsw.New(), searchengine.Options{}))
				return newDistManager(eng, newSharedServerFake().viewFor(
					graphSelector(kgtypes.GraphCode, "republish"), ""), c,
					graphSelector(kgtypes.GraphCode, "republish"), hnsw.New().Name())
			},
		},
		{
			name: "bm25",
			blob: func(t *testing.T) searchengine.SegmentBlob { return consolidatedBM25Blob(t, docs) },
			mgr: func(t *testing.T, c segmentL2Cache) republishArm {
				t.Helper()
				eng := closeOnCleanup(t, searchengine.New[bm25.Query, *bm25.CorpusStats](bm25.New(), searchengine.Options{}))
				return newDistManager(eng, newSharedServerFake().viewFor(
					graphSelector(kgtypes.GraphCode, "republish"), ""), c,
					graphSelector(kgtypes.GraphCode, "republish"), bm25.New().Name())
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cache := newDiskSegmentCache(t.TempDir(), 0, adviceRandom)
			merged := tc.blob(t)
			cache.Put(merged.ID, merged.Bytes)

			arm := tc.mgr(t, cache)

			// Import a HEAP copy, exactly as a merge publishes through newEntry.
			heap, ok := cache.Get(merged.ID)
			require.True(t, ok)
			require.NoError(t, arm.importBlob(searchengine.SegmentBlob{ID: merged.ID, Bytes: heap}))

			//nolint:gosec // G103: reading a backing pointer IS the assertion; test-only
			heapBase := uintptr(unsafe.Pointer(unsafe.SliceData(heap)))

			// KNOWN-NEGATIVE: the published payload starts backed by the HEAP
			// slice we imported. Without this the swap below proves nothing,
			// because a build that already published mappings would pass it.
			require.Equal(t, heapBase, arm.publishedBase(t, merged.ID),
				"control: the payload must start backed by the imported heap slice")

			arm.remap(merged)

			// THE SWAP HAPPENED: the payload is no longer the heap slice, and
			// the remap reported success rather than falling back to pending.
			//
			// The comparison is against the HEAP slice rather than against a
			// mapping taken here, and that is not a weaker assertion but the only
			// correct one: each GetMapped mmaps the file AGAIN at a fresh address,
			// so a pointer taken from a second mapping could never equal the one
			// the entry holds even when the republication is perfect. What is
			// observable is that the payload moved OFF the heap slice, and that
			// remapMerged's only replacement is the mapping it obtained.
			require.NotEqual(t, heapBase, arm.publishedBase(t, merged.ID),
				"remapMerged must republish the entry off the heap copy, as a MAPPING of the cached file")
			require.Empty(t, arm.pendingIDs(),
				"the republication must have SUCCEEDED, not been recorded as pending")

			// The bytes are unchanged by the swap.
			require.Equal(t, heap, arm.publishedBytes(t, merged.ID),
				"republishing must not change what the segment contains")
		})
	}
}

// republishArm is the non-generic view this test needs of either format's pool.
type republishArm interface {
	importBlob(searchengine.SegmentBlob) error
	remap(searchengine.SegmentBlob)
	publishedBase(*testing.T, searchengine.SegmentID) uintptr
	publishedBytes(*testing.T, searchengine.SegmentID) []byte
	pendingIDs() []searchengine.SegmentID
}

func (m *distManager[Q, S]) importBlob(b searchengine.SegmentBlob) error {
	return m.engine.Import([]searchengine.SegmentBlob{b}, nil)
}

func (m *distManager[Q, S]) remap(b searchengine.SegmentBlob) { m.remapMerged(b) }

func (m *distManager[Q, S]) pendingIDs() []searchengine.SegmentID { return m.pendingRemapIDs() }

// publishedBytes returns the published payload's encoded bytes.
func (m *distManager[Q, S]) publishedBytes(t *testing.T, id searchengine.SegmentID) []byte {
	t.Helper()
	for _, b := range m.engine.Export() {
		if b.ID == id {
			return b.Bytes
		}
	}
	t.Fatalf("segment %s is not published", id)
	return nil
}

// publishedBase returns the backing pointer of the published payload's encoded
// bytes. On a MAPPED payload Encode returns the mapping itself; on a heap-backed
// one it returns heap memory, so the two are distinguishable by address even
// though they are byte-identical.
func (m *distManager[Q, S]) publishedBase(t *testing.T, id searchengine.SegmentID) uintptr {
	t.Helper()
	for _, b := range m.engine.Export() {
		if b.ID == id {
			require.NotEmpty(t, b.Bytes)
			//nolint:gosec // G103: reading a backing pointer IS the assertion; test-only
			return uintptr(unsafe.Pointer(unsafe.SliceData(b.Bytes)))
		}
	}
	t.Fatalf("segment %s is not published", id)
	return 0
}
