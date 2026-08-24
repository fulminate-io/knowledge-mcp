// SPDX-License-Identifier: Apache-2.0

package bm25

import (
	"math"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestEncodeRefusesOversizeBlob proves the u32 offset ceiling is a LIVE guard
// that fails loud with the computed size, rather than silently truncating an
// offset and producing a blob whose sections point at the wrong bytes.
//
// A blob that genuinely crosses 4 GiB is not constructible in a test: the size
// is dominated by four bytes per field per document, so reaching the ceiling
// needs on the order of 215 million documents. The fixture therefore lowers the
// ceiling and drives the REAL check through the REAL encode path.
//
// The control that makes that honest is the first assertion: the SHIPPED ceiling
// is asserted to be the full u32 range before the fixture lowers it, so a
// permanently-lowered ceiling — which would make every large segment fail in
// production — cannot hide behind this test.
func TestEncodeRefusesOversizeBlob(t *testing.T) {
	require.Equal(t, int64(math.MaxUint32), v2MaxBlobBytes,
		"the shipped ceiling must be the full u32 range; this test lowers it and must not mask a lowered default")

	acc := buildAccumulator(t, sampleDocs())

	// Known-positive: the unrestricted encode succeeds, so a failure below is the
	// ceiling firing and not the fixture being unencodable for some other reason.
	full, err := encodeSegmentV2(acc, defaultDictKind)
	require.NoError(t, err)
	require.NotEmpty(t, full)

	original := v2MaxBlobBytes
	t.Cleanup(func() { v2MaxBlobBytes = original })
	v2MaxBlobBytes = int64(len(full)) - 1

	_, err = encodeSegmentV2(acc, defaultDictKind)
	require.Error(t, err)
	require.Contains(t, err.Error(), strconv.Itoa(len(full)),
		"the error must name the COMPUTED size so an operator can see how far past the ceiling the segment is")
	require.Contains(t, err.Error(), strconv.FormatInt(v2MaxBlobBytes, 10),
		"the error must name the ceiling it exceeded")
}

// TestEncodeRejectsUnknownDictKind pins that an out-of-range dictionary kind is
// refused at the writer rather than producing a blob no reader can interpret.
func TestEncodeRejectsUnknownDictKind(t *testing.T) {
	acc := buildAccumulator(t, sampleDocs())
	for _, kind := range []byte{dictFlat, dictBlocked, dictHash} {
		_, err := encodeSegmentV2(acc, kind)
		require.NoError(t, err, "kind %d must encode", kind)
	}
	_, err := encodeSegmentV2(acc, dictHash+1)
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown dictionary kind")
}
