// SPDX-License-Identifier: Apache-2.0

package hnsw

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestV2SegmentsAreRejectedWithTheRebuildRemedy is the migration contract: a
// segment written by the OLD layout must be refused, and the refusal must tell
// the operator what to do about it.
//
// THE INPUT IS A CHECKED-IN FIXTURE AND THAT IS LOAD-BEARING. This tree cannot
// write a v2 blob any more — the v2 writer was deleted when the offset-addressed
// layout replaced it. A test that generated its own input would be producing a v3
// blob and asserting v3 is rejected, which is not the migration property and would
// still pass with the version check removed. See testdata/README.md.
//
// The remedy half is asserted, not just the rejection. "Segments are derived
// caches, so torn or superseded state means rebuild, not loss" is only true for
// an operator who is TOLD to rebuild; an error that merely says "unsupported"
// leaves them with a broken index and no next step.
func TestV2SegmentsAreRejectedWithTheRebuildRemedy(t *testing.T) {
	t.Parallel()

	blob, err := os.ReadFile(filepath.Join("testdata", "hnsw_v2_segment.seg"))
	require.NoError(t, err, "the v2 fixture must be checked in — it cannot be regenerated from this tree")
	require.NotEmpty(t, blob)
	require.Equal(t, byte(2), blob[0],
		"the fixture must really be v2; a fixture that drifted to another version tests nothing")

	_, err = Format{}.Decode(blob)
	require.Error(t, err, "a v2 blob must be REJECTED, not decoded")

	msg := err.Error()
	// Pinned to the PHRASE, not the bare digit: Contains(msg, "3") would be satisfied
	// by any byte count or offset that happens to contain a 3, which is most of them.
	require.Contains(t, msg, "want "+strconv.Itoa(int(serialVersionOffsets)),
		"the error names the version this build wants, so the operator can tell which side is stale")
	require.Contains(t, msg, "version 2",
		"the error names the version it FOUND — the other half of 'which side is stale'")
	require.Contains(t, msg, "rebuild it from source",
		"the error carries the REMEDY, not just the rejection")

	// KNOWN-POSITIVE in the same run: the identical Decode call ACCEPTS a blob this
	// tree writes. Without it, a Decode that rejected everything — or one wired to a
	// nil graph — would satisfy every assertion above.
	seg, _, err := Format{}.Build(vecDocs(8))
	require.NoError(t, err)
	fresh, err := seg.Encode()
	require.NoError(t, err)
	require.Equal(t, serialVersionOffsets, fresh[0], "the current writer emits the offset-addressed version")
	_, err = Format{}.Decode(fresh)
	require.NoError(t, err, "the same Decode accepts a current-format blob — so the v2 rejection is about the VERSION")
}
