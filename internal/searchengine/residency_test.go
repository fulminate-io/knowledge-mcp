package searchengine

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// residencyFixture builds an index holding TWO sealed mock segments whose member
// ids have DELIBERATELY DIFFERENT LENGTHS. Equal-length ids would let a wrong
// model pass: with every id the same size, the per-member id-bytes term and the
// per-member constant term become indistinguishable, so a formula that dropped
// one and doubled the other would still land on the right total. content sets
// each row's payload text, which changes the ENCODED blob size without touching
// the member set — the lever the blob-independence assertion pulls.
func residencyFixture(t *testing.T, content string) *SegmentedIndex[mockQuery, mockStats] {
	t.Helper()
	e := closeOnCleanup(t, New[mockQuery, mockStats](mockFormat{}, Options{MinSegmentDocs: 1}))
	require.NoError(t, e.Add([]Document{
		{ID: "a", Fields: map[string]string{FieldContent: content}},
		{ID: "bbbbbbbbbb", Fields: map[string]string{FieldContent: content}},
	}))
	require.NoError(t, e.Add([]Document{
		{ID: "ccc", Fields: map[string]string{FieldContent: content}},
	}))
	return e
}

// TestResidentHeapBytesCountsMembersNotBlob pins the residency model to the
// formula ResidentHeapBytes documents, and pins the property that distinguishes
// this meter from the blob-bytes meter it replaces.
//
// THE EXPECTATION IS DERIVED BY HAND FROM THE FIXTURE, not by calling
// membersHeapBytes. Computing it with the production helper would be an
// identity check — the thing under test supplying its own answer key — and
// would pass just as happily with the helper wrong.
func TestResidentHeapBytesCountsMembersNotBlob(t *testing.T) {
	e := residencyFixture(t, "short")

	// Segment 1 holds ids "a" (1 byte) and "bbbbbbbbbb" (10 bytes) over 2
	// ordinals, so its bitset is one 64-bit word. Segment 2 holds "ccc" (3
	// bytes) over 1 ordinal, also one word.
	const (
		seg1Rows, seg2Rows = 2, 1
		seg1IDBytes        = 1 + 10
		seg2IDBytes        = 3
		seg1Members        = 2
		seg2Members        = 1
		bitsetWords        = 1 + 1 // one word per segment
	)
	want := int64(seg1Rows+seg2Rows)*mockSegmentHeapBytesPerRow + // payload term
		int64(seg1IDBytes+seg2IDBytes) + // member id bytes
		int64(seg1Members+seg2Members)*memberEntryOverheadBytes + // per-member constant
		int64(bitsetWords)*8 // liveness bitsets

	got := e.ResidentHeapBytes()
	require.Equal(t, want, got,
		"ResidentHeapBytes must equal the documented three-term model exactly")

	// KNOWN-POSITIVE FLOORS. Each term is individually non-zero, so a total that
	// happened to match while a term was silently zero cannot pass unnoticed.
	require.Positive(t, int64(seg1Rows+seg2Rows)*mockSegmentHeapBytesPerRow,
		"payload term must be non-zero or this test cannot detect its removal")
	require.Positive(t, memberEntryOverheadBytes,
		"per-member term must be non-zero or this test cannot detect its removal")
}

// TestResidentHeapBytesIgnoresBlobSize is the property that separates this meter
// from the one it replaces: growing the ENCODED blob by two orders of magnitude
// while holding the member set identical must not move the number at all. The
// retired blob-bytes meter would have moved with it.
func TestResidentHeapBytesIgnoresBlobSize(t *testing.T) {
	small := residencyFixture(t, "x")
	large := residencyFixture(t, strings.Repeat("x", 100_000))

	// CONTROL: the fixtures really do differ in encoded size. Without this the
	// equality below could hold because both blobs were the same all along.
	smallBlob, err := small.set.Load().entries[0].payload.Encode()
	require.NoError(t, err)
	largeBlob, err := large.set.Load().entries[0].payload.Encode()
	require.NoError(t, err)
	require.Greater(t, len(largeBlob), 50*len(smallBlob),
		"fixture control: the large fixture must encode far bigger, or this test proves nothing")

	require.Equal(t, small.ResidentHeapBytes(), large.ResidentHeapBytes(),
		"heap model must be independent of encoded blob size")
}
