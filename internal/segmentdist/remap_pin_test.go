// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// TestRemapPendingPinsTheMappedPayload is the USE-AFTER-UNMAP CATCHER for the
// blob a pending remap retains.
//
// WHY IT HAS TO BE BEHAVIORAL rather than a grep for the field's type: a raw
// []byte compiles, satisfies every existing test, and faults only when the
// cleanup happens to run. The bound's one additive repair re-Puts the retained
// bytes, and those bytes are a MAPPING owned by the resident entry — holding a
// slice does not make that entry reachable, so a retained raw slice could be read
// after its mapping was released. Retaining the whole SegmentBlob is what fixes
// it, because the blob's keepAlive pins the entry and the entry pins the mapping.
//
// THE ENTRY IS DELIBERATELY MADE UNREACHABLE. Unloading the segment removes it
// from the published set, which is the state the hazard needs: after that, the
// ONLY thing keeping the mapping alive is the reference the pending remap holds.
// A test that left the entry published would pin the mapping through the segment
// set and pass against the defective shape.
func TestRemapPendingPinsTheMappedPayload(t *testing.T) {
	dm, ic, merged := remapFixture(t)

	// THE ANSWER KEY, copied now and independent of any mapping. Comparing the
	// re-Put file against the blob the drain retained would be comparing the
	// suspect against itself.
	want := append([]byte{}, merged.Bytes...)

	// A HEALTHY remap first, so the published payload actually becomes a mapping
	// of the cached file. Without this the retained bytes stay heap-owned and the
	// hazard this test exists for is not present at all.
	dm.remapMerged(merged)
	require.Empty(t, dm.pendingRemapIDs(),
		"control: the healthy remap must succeed, or the payload never became a mapping")

	exported := dm.engine.Export()
	require.Len(t, exported, 1)
	mapped := exported[0]
	require.Equal(t, want, mapped.Bytes, "control: the mapped payload must hold the same bytes")

	// Now break the mapping arm and fail a republication, so the blob is retained.
	ic.mu.Lock()
	ic.failMapping = true
	ic.mu.Unlock()

	require.True(t, mapped.PinsMapping(),
		"control: an exported mapping-backed blob must carry its pin, or the retention assertion below is vacuous")

	dm.remapMerged(mapped)
	require.Equal(t, []searchengine.SegmentID{mapped.ID}, dm.pendingRemapIDs(),
		"the failed remap must be recorded as pending, or nothing is retained to read later")

	// THE DISCRIMINATING ASSERTION, and it is the one that carries this test.
	//
	// THE BYTE-IDENTITY LEG BELOW DOES NOT DISCRIMINATE, measured rather than
	// assumed: replacing the retention with a rebuilt SegmentBlob carrying the same
	// slices but no pin — which is what any holder outside searchengine gets, since
	// keepAlive cannot be set from another package — leaves this whole test GREEN.
	// The mapping simply is not collected within the test's lifetime, however hard
	// it is asked to be. Ten GC cycles with Gosched between them were not enough,
	// and a lifetime test that depends on winning that race would be flaky rather
	// than strict.
	//
	// So the pin is asserted DIRECTLY. This fails immediately and deterministically
	// on the unpinned shape, which is exactly the defect the retention change
	// exists to prevent.
	dm.resMu.Lock()
	retained := dm.remapPending[mapped.ID].blob
	dm.resMu.Unlock()
	require.True(t, retained.PinsMapping(),
		"the pending remap retained a payload WITHOUT the reference that keeps its mapping alive — "+
			"holding the bytes does not keep the entry reachable, so this blob can be read after its unmap")

	// Drop the entry from the published set and every local reference to it, then
	// force collection hard enough that any runtime.AddCleanup callback keyed on
	// the entry's reachability has had the chance to run.
	dm.engine.Unload([]searchengine.SegmentID{mapped.ID})
	mapped.Bytes = nil
	mapped.Envelope = nil
	for range 10 {
		runtime.GC()
		runtime.Gosched()
	}

	putsBefore := countOps(ic, "put", merged.ID)
	for range remapMaxAttempts + 2 {
		require.NoError(t, dm.drainRemapPending())
	}
	require.Equal(t, putsBefore+1, countOps(ic, "put", merged.ID),
		"the bound must attempt exactly one additive re-Put, or the read below never happened")

	// THE ASSERTION: the bytes the drain re-Put are still the merged segment's.
	// A retained slice whose mapping had been released would write garbage here,
	// or fault before reaching it.
	stored, ok := ic.Get(merged.ID)
	require.True(t, ok, "the re-Put did not leave a readable stored blob")
	require.Equal(t, want, stored,
		"the re-Put wrote bytes that are not the merged segment — the retained payload did not survive its entry")
}
