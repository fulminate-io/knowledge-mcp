// SPDX-License-Identifier: Apache-2.0

package bm25

import (
	"encoding/binary"
	"testing"

	"github.com/stretchr/testify/require"
)

// buildV1Blob constructs a REAL version-1 segment blob by hand.
//
// The v1 encoder is gone — deleted with the layout it wrote — so this fixture
// carries the bytes itself, laid out to the retired format's own documented
// shape: a version byte, then the member table, then the field table, then the
// per-segment document frequencies, every integer little-endian.
//
// Building it rather than checking in a binary keeps the fixture readable and,
// more importantly, keeps it HONEST: a reader can see it really is a v1 blob and
// not an arbitrary byte string that happens to start with a 1.
func buildV1Blob() []byte {
	var buf []byte
	buf = append(buf, 1) // version

	// members: u32 count, then per member u16 length + bytes.
	members := []string{"doc-a", "doc-b"}
	buf = binary.LittleEndian.AppendUint32(buf, uint32(len(members)))
	for _, id := range members {
		buf = binary.LittleEndian.AppendUint16(buf, uint16(len(id)))
		buf = append(buf, id...)
	}

	// fields: u32 count, then per field name, boost, b, totalTokens, doc
	// lengths and postings.
	buf = binary.LittleEndian.AppendUint32(buf, 1)
	name := "content"
	buf = binary.LittleEndian.AppendUint16(buf, uint16(len(name)))
	buf = append(buf, name...)
	buf = binary.LittleEndian.AppendUint64(buf, mathFloatBits(1))    // boost
	buf = binary.LittleEndian.AppendUint64(buf, mathFloatBits(0.75)) // b
	buf = binary.LittleEndian.AppendUint64(buf, 7)                   // totalTokens
	buf = binary.LittleEndian.AppendUint32(buf, uint32(len(members)))
	for range members {
		buf = binary.LittleEndian.AppendUint32(buf, 3)
	}
	buf = binary.LittleEndian.AppendUint32(buf, 1) // one term
	term := "alpha"
	buf = binary.LittleEndian.AppendUint16(buf, uint16(len(term)))
	buf = append(buf, term...)
	buf = binary.LittleEndian.AppendUint32(buf, 1) // one posting
	buf = binary.LittleEndian.AppendUint32(buf, 0) // docID
	buf = binary.LittleEndian.AppendUint16(buf, 2) // tf

	// docFreq: u32 count, then per term u16 length + bytes + u64 frequency.
	buf = binary.LittleEndian.AppendUint32(buf, 1)
	buf = binary.LittleEndian.AppendUint16(buf, uint16(len(term)))
	buf = append(buf, term...)
	buf = binary.LittleEndian.AppendUint64(buf, 1)
	return buf
}

// TestRejectsV1BlobWithRebuildRemedy pins the migration itself. There is no
// converter and there is not meant to be one: segments are a derived cache with
// a production heal path, so the migration is to REFUSE the old layout and let
// that path rebuild — the same choice, for the same reason, that the vector
// format made when it bumped its own version.
//
// The error must carry BOTH halves, and the second is the one an operator acts
// on: the version actually seen, and the remedy. An error that says only
// "unsupported version" tells someone staring at a log that something is wrong
// and nothing about what to do, and this fires exactly once per upgrade — the
// moment when the message is the whole user experience.
func TestRejectsV1BlobWithRebuildRemedy(t *testing.T) {
	v1 := buildV1Blob()
	require.Equal(t, byte(1), v1[0], "the fixture must really be a version-1 blob")

	seg, err := openSegmentV2(v1)
	require.Error(t, err, "a version-1 blob must be refused")
	require.Nil(t, seg, "a refused blob must not yield a partially read segment")
	require.Contains(t, err.Error(), "version 1", "the error must name the version it saw")
	require.Contains(t, err.Error(), "rebuild", "the error must name the remedy")

	// Format.Decode is the production entry point, so the same refusal must
	// reach a caller through it and not only through the reader.
	decoded, err := Format{}.Decode(v1)
	require.Error(t, err)
	require.Nil(t, decoded)
	require.Contains(t, err.Error(), "rebuild")

	// KNOWN-POSITIVE: the current layout still opens, so the refusal above is
	// the version gate and not a reader that rejects everything.
	good, err := encodeSegmentV2(buildAccumulator(t, sampleDocs()), defaultDictKind)
	require.NoError(t, err)
	ok, err := openSegmentV2(good)
	require.NoError(t, err)
	require.NotNil(t, ok)
}
