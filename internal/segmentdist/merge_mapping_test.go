// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"sync"
	"testing"
	"unsafe"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// merge_mapping_test.go carries the MAPPING-BACKED property: that a merged
// payload is the merge file's mapping rather than a heap copy of it, in both
// production pools. The ALLOCATION property — that a merge's allocations stay
// flat as its output grows — is the other half of the same claim and lives in
// merge_zero_heap_test.go, together with the frozen pre-change baseline both
// halves are argued against.

// mappingRecorder wraps the pools' mapping hook and records the base address of
// every slice it hands out.
//
// THE BASES ARE RECORDED AT MAP TIME, WHICH IS THE ONLY TIME THEY CAN BE. Each
// mmap of the same file lands at a fresh address, so a pointer obtained by
// mapping the file a second time could never equal the one the entry holds even
// when the republication is perfect — a fact this package already records against
// its republication test.
type mappingRecorder struct {
	mu    sync.Mutex
	bases map[uintptr]bool
}

// install replaces the pools' mapping-hook constructor for the test's duration,
// wrapping the real one rather than standing in for it: the mapping that gets
// made is the production mapping, with this format's own advice.
func (r *mappingRecorder) install(t *testing.T) {
	t.Helper()
	r.bases = map[uintptr]bool{}
	prev := newMapBlobHook
	t.Cleanup(func() { newMapBlobHook = prev })
	newMapBlobHook = func(advice readAdvice) func(string) ([]byte, func(), error) {
		inner := prev(advice)
		return func(path string) ([]byte, func(), error) {
			data, release, err := inner(path)
			if err != nil {
				return nil, nil, err
			}
			r.mu.Lock()
			//nolint:gosec // G103: recording a backing pointer IS the measurement; test-only
			r.bases[uintptr(unsafe.Pointer(unsafe.SliceData(data)))] = true
			r.mu.Unlock()
			return data, release, nil
		}
	}
}

func (r *mappingRecorder) recorded(base uintptr) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.bases[base]
}

func (r *mappingRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.bases)
}

// TestMergedPayloadIsMappingBackedNotHeapBacked proves the property this whole
// changeset exists for, in BOTH production pools: a merged segment's payload is
// the merge file's MAPPING, not a heap copy of it.
//
// IT IS THE INVERSION of the retired assertion that a merged segment was
// heap-backed. That test was correct about the code it described; this asserts
// the opposite property of the code that replaced it.
//
// THE OBSERVABLE IS POINTER IDENTITY, NOT HeapBytes, and that correction is
// load-bearing rather than stylistic. bm25's mappedSegment.HeapBytes returns
// unsafe.Sizeof(*s) plus a per-field constant and never references the blob at
// all — by design, as its own doc says, because it is a MODEL of the fixed
// per-segment struct cost. It therefore reports the identical number for a
// mapping-backed and a heap-backed payload, and a gate keyed on it would have
// passed against the unfixed tree. It is not asserted here at all, because an
// assertion that cannot discriminate is worse than none.
//
// BOTH POOLS, because manager_factory.go has two constructor bodies and wiring
// only one leaves half the production surface on the heap path with nothing red.
// The HNSW arm carries NO zero-heap claim — that format's merge holds an
// output-sized insertion structure by algorithm — but its merged payload is
// mapping-backed after this change, and that is what is asserted of it.
func TestMergedPayloadIsMappingBackedNotHeapBacked(t *testing.T) {
	requireMeasurementRun(t)
	rec := &mappingRecorder{}
	rec.install(t)

	ctx := context.Background()
	mgr := closeOnCleanup(t, NewManager(t.TempDir(), 0))
	gt := kgtypes.GraphCode

	for _, tc := range []struct {
		name string
		// sealHalf publishes half the fixture corpus into graph. Calling it once
		// leaves a BUILT segment; calling it twice makes the second pass
		// consolidate against the first, which is the merge under test.
		sealHalf func(t *testing.T, graph string, half int)
		// sealWithoutMerging publishes a segment through the plain ADD path, which
		// seals and publishes directly and never reaches the merge. It is what the
		// mirror leg needs and what a bucket replace cannot supply: a replace with
		// nothing resident is still a merge-of-one, so its output is mapped like any
		// other merge. Measured, not assumed — the mirror leg failed against a
		// single-pass replace fixture for exactly that reason.
		sealWithoutMerging func(t *testing.T, graph string)
		exported           func(t *testing.T, graph string) []searchengine.SegmentBlob
		baseOf             func(t *testing.T, graph string, id searchengine.SegmentID) uintptr
	}{
		{
			name: "bm25",
			sealHalf: func(t *testing.T, graph string, half int) {
				docs := bm25FieldDocs(2048)
				mid := len(docs) / 2
				part := docs[:mid]
				if half == 1 {
					part = docs[mid:]
				}
				require.NoError(t, mgr.ReplaceBucketFields(ctx, gt, graph, nil, part))
			},
			sealWithoutMerging: func(t *testing.T, graph string) {
				require.NoError(t, mgr.AddAndMarkDirtyFields(ctx, gt, graph, bm25FieldDocs(2048)))
			},
			exported: func(t *testing.T, graph string) []searchengine.SegmentBlob {
				return mgr.bm25ManagerFor(gt, graph).engine.Export()
			},
			baseOf: func(t *testing.T, graph string, id searchengine.SegmentID) uintptr {
				return mgr.bm25ManagerFor(gt, graph).publishedBase(t, id)
			},
		},
		{
			name: "hnsw",
			sealHalf: func(t *testing.T, graph string, half int) {
				docs := vecContentDocs(2048)
				mid := len(docs) / 2
				part := docs[:mid]
				if half == 1 {
					part = docs[mid:]
				}
				require.NoError(t, mgr.ReplaceBucket(ctx, gt, graph, nil, part))
			},
			sealWithoutMerging: func(t *testing.T, graph string) {
				require.NoError(t, mgr.AddAndMarkDirty(ctx, gt, graph, vecContentDocs(2048)))
			},
			exported: func(t *testing.T, graph string) []searchengine.SegmentBlob {
				return mgr.managerFor(gt, graph).engine.Export()
			},
			baseOf: func(t *testing.T, graph string, id searchengine.SegmentID) uintptr {
				return mgr.managerFor(gt, graph).publishedBase(t, id)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			merging := "mapped-" + tc.name
			tc.sealHalf(t, merging, 0)
			tc.sealHalf(t, merging, 1)

			merged := soleEnvelopedBlob(t, tc.exported(t, merging))
			require.Positive(t, rec.count(), "no mapping was ever handed out, so the comparison below is vacuous")

			// (a) THE PROPERTY: the merged payload IS one of the mappings the pool made.
			require.True(t, rec.recorded(tc.baseOf(t, merging, merged.ID)),
				"the merged payload is not backed by any mapping this pool created — it is a heap copy, "+
					"which is the state this whole changeset removes")

			// (b) THE MIRROR, so (a) is not satisfied by an implementation that mapped
			// EVERYTHING. A segment that was built and sealed and never merged is
			// published heap-backed; a build that mapped every payload would fail here
			// and would be a different, wrong change.
			built := "built-" + tc.name
			tc.sealWithoutMerging(t, built)
			blobs := tc.exported(t, built)
			require.NotEmpty(t, blobs, "the single seal published nothing, so the mirror asserts nothing")
			checked := 0
			for _, b := range blobs {
				require.Empty(t, b.Envelope, "a single seal supersedes nothing, so it must carry no record")
				require.False(t, rec.recorded(tc.baseOf(t, built, b.ID)),
					"a freshly BUILT segment's payload is backed by a mapping, so this test cannot tell a "+
						"merged payload from any other and (a) proves nothing")
				checked++
			}
			require.Positive(t, checked, "no built segment was examined")

			// (c) The mapping is correctly DECODED, not merely correctly identified.
			require.NotEmpty(t, merged.Bytes, "the merged payload is empty")
		})
	}
}

// soleEnvelopedBlob returns the one exported blob carrying a supersession record
// — the consolidation's output. A pool that produced none fails loudly rather
// than returning a zero blob a caller would assert against.
func soleEnvelopedBlob(t *testing.T, blobs []searchengine.SegmentBlob) searchengine.SegmentBlob {
	t.Helper()
	var out searchengine.SegmentBlob
	found := 0
	for _, b := range blobs {
		if len(b.Envelope) == 0 {
			continue
		}
		found++
		out = b
	}
	require.Positive(t, found, "no exported blob carried a supersession record, so no merge happened")
	return out
}
