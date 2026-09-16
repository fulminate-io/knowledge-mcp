// SPDX-License-Identifier: Apache-2.0

package bm25

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// merge_writer_padding_test.go — the writer's two provenance rules, asserted on the
// writer directly rather than through a merge.
//
// WHY AT THIS LEVEL. Through a merge these rules are visible only as the ABSENCE of
// a corrupt blob, and an absence is the weakest evidence there is: a shape that
// stopped reaching the arm would read exactly like a rule that held. Here the arm is
// driven on purpose and what it did to the bytes is read back.

// paddingSink is a sink whose final bytes a row can inspect. It forwards to a real
// file for the reason armOrderSink does: a sink that swallowed the bytes would let a
// writer that produced nothing satisfy every assertion.
func paddingSink(t *testing.T) *armOrderSink {
	t.Helper()
	return newArmOrderSink(t)
}

// readBack returns n bytes of the sink's file from off.
func readBack(t *testing.T, sink *armOrderSink, off int64, n int) []byte {
	t.Helper()
	buf := make([]byte, n)
	_, err := sink.ReadAt(buf, off)
	require.NoError(t, err)
	return buf
}

// TestPatchNeverPadsOverABytesAnotherWriteOwns is the DEFECT'S OWN SHAPE, reduced to
// three stores: a run that ends where a second structure begins, that second
// structure written, and then a patch landing a small forward gap past the first
// run's end.
//
// THE PATCH MUST NOT REACH ACROSS THE GAP. Before the provenance rule, the run
// absorbed the gap as zeros and flushed them over bytes the second structure had
// already put on disk — which is exactly how a field's block-index entries became
// zeros and a reader resolved those blocks at blob offset 0.
func TestPatchNeverPadsOverABytesAnotherWriteOwns(t *testing.T) {
	sink := paddingSink(t)
	w := newMergeWriter(sink, 0)

	// (1) A live run holding the first structure, ending at 16.
	w.store(0, []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16})
	// (2) The SECOND structure, written and flushed to the sink: eight bytes at 16,
	// the range a gap-absorbing pad would cover.
	w.store(16, []byte{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff, 0xa1, 0xb2})
	w.flushOverlapping(16, 24)
	require.NoError(t, w.err)
	require.Equal(t, []byte{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff, 0xa1, 0xb2}, readBack(t, sink, 16, 8),
		"precondition: the second structure is on disk before the patch below is made")

	// (3) THE PATCH, eight bytes past the first run's end — a gap of exactly
	// mergeRunMaxGap, which is what a three-block field's first block-index patch
	// opens.
	w.store(24, []byte{0x11, 0x22, 0x33, 0x44})
	w.flushAll()
	require.NoError(t, w.err)

	require.Equal(t, []byte{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff, 0xa1, 0xb2}, readBack(t, sink, 16, 8),
		"the patch must not have filled the gap with zeros: those bytes belong to a structure another store wrote")
	require.Equal(t, []byte{0x11, 0x22, 0x33, 0x44}, readBack(t, sink, 24, 4), "and the patch itself landed")
}

// TestExtendAcrossALiveRunFlushesItFirst covers the OTHER hole the extend arm had,
// which is independent of padding: a store that extends one run across a range a
// DIFFERENT live run holds used to leave both live and overlapping, and the flush
// order — not the store order — decided the bytes.
//
// THE LATER STORE MUST WIN, because that is what store means. Without the flush the
// two runs reach the sink in slot order at flushAll, so the run holding the OLDER
// value is written last and the newer store is lost.
func TestExtendAcrossALiveRunFlushesItFirst(t *testing.T) {
	sink := paddingSink(t)
	w := newMergeWriter(sink, 0)

	// Slot 0 takes a run at [0,16). Slot 1 takes a second run at [24,32) holding the
	// OLD value of that range — opened by its own arm because a patch eight bytes
	// past a live run no longer extends one.
	w.store(0, make([]byte, 16))
	w.store(24, []byte{9, 9, 9, 9, 9, 9, 9, 9})
	live := 0
	for i := range w.runs {
		if w.runs[i].live {
			live++
		}
	}
	require.Equal(t, 2, live, "precondition: two live runs, which is what makes the extend below cross one")

	// THE SUBJECT: a store contiguous with slot 0's run whose range REACHES OVER slot
	// 1's, carrying a NEW value for those bytes.
	w.store(16, []byte{7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7})
	w.flushAll()
	require.NoError(t, w.err)

	require.Equal(t, []byte{7, 7, 7, 7, 7, 7, 7, 7}, readBack(t, sink, 24, 8),
		"the LAST store of a range is what the sink must end up holding; a stale run flushing after it is the defect")
}

// TestTailAppendBelowThePrefixIsRefused is the floor check on the one store that may
// pad. It cannot happen by construction — the tail starts at the planned prefix end
// and only advances — so the guard exists to fail loudly if a future caller mistakes
// a patch for an append, and this row is what proves the guard is armed.
func TestTailAppendBelowThePrefixIsRefused(t *testing.T) {
	sink := paddingSink(t)
	w := newMergeWriter(sink, 128)

	// CONTROL FIRST: an append at or above the floor is accepted, so the refusal
	// below is about the offset rather than about the call.
	w.storeAppend(128, []byte{1, 2, 3, 4})
	require.NoError(t, w.err, "an append at the floor is legitimate")

	w.storeAppend(64, []byte{5, 6, 7, 8})
	require.Error(t, w.err, "an append below the planned prefix end must fail the merge rather than pad over the prefix")
	require.Contains(t, w.err.Error(), "REFUSING a tail append at offset 64")
	require.Contains(t, w.err.Error(), "below the planned prefix end 128")
}
