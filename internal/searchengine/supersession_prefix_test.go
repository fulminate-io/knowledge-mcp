package searchengine

import (
	"testing"
	"unsafe"

	"github.com/stretchr/testify/require"
)

// TestSupersessionPrefixBytesAreUnchanged pins the STORED ENVELOPE byte for byte.
//
// THE EXPECTATION IS WRITTEN FROM THE FORMAT, NOT CAPTURED FROM A RUN, which is
// what makes it checkable by reading it. Every byte below is derivable from the
// layout constants: supHeaderLen is 20, the two ids cost (2+2) and (2+3), giving
// a body of 29, rounded up to the 8-byte payload alignment to 32. A golden
// captured from the encoder would have ratified whatever the encoder happened to
// do, including a change to the layout this test exists to forbid.
//
// The envelope is the only part of a stored blob this changeset touches, and it
// must not move: an already-released reader locates the payload from these bytes.
func TestSupersessionPrefixBytesAreUnchanged(t *testing.T) {
	t.Parallel()

	rec := supersessionRecord{Superseded: []SegmentID{"aa"}, Cohort: []SegmentID{"bbb"}}
	prefix := encodeSupersessionPrefix(rec)

	want := []byte{
		0x00, 'S', 'E', 'G', 'S', 'U', 'P', 0x01, // magic; the leading 0x00 is what an old reader refuses on
		32, 0, 0, 0, // payloadOff = supHeaderLen(20) + 4 + 5 = 29, rounded up to 32
		1, 0, 0, 0, // supersededCount
		1, 0, 0, 0, // cohortCount
		2, 0, 'a', 'a', // superseded[0]
		3, 0, 'b', 'b', 'b', // cohort[0]
		0, 0, 0, // padding to the 8-byte alignment
	}
	require.Len(t, want, 32, "the expectation itself must be 32 bytes, or the arithmetic above is wrong")
	require.Equal(t, want, prefix, "the stored envelope's bytes moved")

	payload := []byte{3, 1, 0, 0, 0xAA, 0xBB, 0xCC, 0xDD}

	t.Run("prefix plus payload round-trips", func(t *testing.T) {
		stored := append(append([]byte{}, prefix...), payload...)
		got, body, err := decodeSupersession(stored)
		require.NoError(t, err)
		require.Equal(t, payload, body, "the format payload must survive byte-for-byte")
		require.Equal(t, rec.Superseded, got.Superseded)
		require.Equal(t, rec.Cohort, got.Cohort)
	})

	t.Run("an empty record yields no envelope at all", func(t *testing.T) {
		// This is what keeps every non-consolidating blob byte-identical to what
		// this engine wrote before records existed: nil prefix, so the stored file
		// is the payload and nothing else.
		require.Nil(t, encodeSupersessionPrefix(supersessionRecord{}))

		env, body, err := splitStoredBlob(payload)
		require.NoError(t, err)
		require.Nil(t, env)
		require.Equal(t, payload, body)
	})

	t.Run("splitStoredBlob is zero-copy on both halves", func(t *testing.T) {
		// THE LENGTHS ARE THE SHAPE CLAIM; THE BACKING IS THE ZERO-COPY CLAIM, and
		// only the second one is the reason this function exists. A splitter that
		// copied would return the right lengths and quietly turn every mapped blob
		// into two heap allocations at the moment of the split.
		stored := append(append([]byte{}, prefix...), payload...)
		env, body, err := splitStoredBlob(stored)
		require.NoError(t, err)
		require.Len(t, env, len(prefix))
		require.Len(t, body, len(payload))

		//nolint:gosec // G103: reading a backing pointer IS the assertion; test-only
		base := uintptr(unsafe.Pointer(unsafe.SliceData(stored)))
		//nolint:gosec // G103: as above
		envBase := uintptr(unsafe.Pointer(unsafe.SliceData(env)))
		//nolint:gosec // G103: as above
		bodyBase := uintptr(unsafe.Pointer(unsafe.SliceData(body)))
		require.Equal(t, base, envBase, "the envelope must be a subslice of the input, not a copy")
		require.Equal(t, base+uintptr(len(prefix)), bodyBase,
			"the payload must be a subslice of the input at the envelope's end, not a copy")

		// And observably so: mutating the input shows through the returned payload.
		stored[len(prefix)] ^= 0xFF
		require.Equal(t, stored[len(prefix)], body[0],
			"a write through the input must be visible through the returned payload")
	})

	t.Run("a payload offset outside the blob is refused", func(t *testing.T) {
		// THE KNOWN-NEGATIVE. Without it every assertion above is satisfied by a
		// decoder that accepts anything, and a decoder that accepts anything hands
		// the format an envelope header to read as an index.
		damaged := append(append([]byte{}, prefix...), payload...)
		damaged[supMagicLen] = 0xFF // payloadOff = 255, past the end of this blob

		_, _, err := decodeSupersession(damaged)
		require.Error(t, err, "an envelope naming a payload past the blob's end must be REFUSED")
		require.Contains(t, err.Error(), "outside a")

		_, _, err = splitStoredBlob(damaged)
		require.Error(t, err, "the splitter must refuse it too, through the same header parse")
	})
}
