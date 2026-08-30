package searchengine

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// supersession_test.go gates the durable supersession record: the ids a consolidated
// blob names as superseded, carried IN the blob so a cold load needs no external state
// to know them.
//
// THE ONE-VERSION-BACK CONTROL IS THE FIRST TEST HERE, deliberately. Every stored blob
// written before this record existed carries no envelope, and the format's own
// versioning discipline supports exactly one direction: a NEW reader accepts OLD bytes,
// while an OLD reader REFUSES bytes it does not recognize (loudly — that is why hnsw's
// float32 flavor took its own version number rather than riding a header tag). So the
// obligation this file pins is that a record-less blob loads BYTE-IDENTICALLY, not that
// an old binary can read a new one.

func TestABlobWithNoRecordLoadsUnchanged(t *testing.T) {
	t.Parallel()

	// Two shapes an old blob can have, both of which must pass straight through: a
	// real format payload (byte 0 is the format's own small version integer) and an
	// arbitrary short one.
	for _, payload := range [][]byte{
		{3, 0, 0, 0, 9, 9, 9, 9},         // an hnsw v3 header start
		{2, 0, 0, 0, 7},                  // a bm25 v2 header start
		{},                               // degenerate
		{0x00, 'S', 'E', 'G'},            // a PREFIX of the magic, too short to be one
		[]byte("not a segment envelope"), // arbitrary
	} {
		rec, body, err := decodeSupersession(payload)
		require.NoError(t, err,
			"a blob with no supersession envelope must decode as itself, never as a malformed one")
		require.True(t, rec.empty(), "and it must report no superseded ids")
		require.Equal(t, payload, body,
			"and the payload handed to the format must be the ORIGINAL bytes — one-version-back means "+
				"byte-identical, not merely parseable")
	}
}

func TestTheRecordRoundTripsAndKeepsThePayloadAligned(t *testing.T) {
	t.Parallel()

	payload := []byte{3, 1, 0, 0, 0xAA, 0xBB, 0xCC, 0xDD, 0xEE}
	ids := []SegmentID{
		"b0000000000000000000000000000000000000000000000000000000000000001",
		"a0000000000000000000000000000000000000000000000000000000000000002",
	}

	cohort := []SegmentID{"c0000000000000000000000000000000000000000000000000000000000000003"}
	blob := storedBlobBytes(supersessionRecord{Superseded: ids, Cohort: cohort}, payload)
	got, body, err := decodeSupersession(blob)
	require.NoError(t, err)
	require.Equal(t, payload, body, "the format payload must survive the envelope byte-for-byte")
	require.ElementsMatch(t, ids, got.Superseded, "and every superseded id must come back")
	require.ElementsMatch(t, cohort, got.Cohort,
		"as must the cohort — a reader that lost it could not tell whether the replacement landed whole")

	// THE OFFSET IS A MULTIPLE OF 8, AND THIS IS NOT COSMETIC. hnsw's mapped reader
	// checks a section's BLOB-RELATIVE offset for alignment and then casts at
	// &blob[off]; the absolute address is base+off, so a payload that started at an
	// odd offset would pass that check and still be a misaligned typed view.
	require.Zero(t, (len(blob)-len(payload))%8,
		"the envelope must be a multiple of 8 bytes so the format payload keeps the alignment its "+
			"reader assumes")
}

func TestTheRecordIsDeterministic(t *testing.T) {
	t.Parallel()

	// A SEGMENT ID IS THE SHA256 OF THESE BYTES, so an encoder that varied on
	// identical input — by iterating a map, say — would break content-addressed dedup
	// and re-key the same merge on every run.
	payload := []byte{3, 0, 0, 0}
	a := storedBlobBytes(supersessionRecord{
		Superseded: []SegmentID{"zzz", "aaa", "mmm"}, Cohort: []SegmentID{"q", "p"}}, payload)
	b := storedBlobBytes(supersessionRecord{
		Superseded: []SegmentID{"mmm", "zzz", "aaa"}, Cohort: []SegmentID{"p", "q"}}, payload)
	require.Equal(t, a, b, "the same id SET must produce the same bytes regardless of input order")
}

func TestAMalformedEnvelopeIsRefusedRatherThanIgnored(t *testing.T) {
	t.Parallel()

	good := storedBlobBytes(
		supersessionRecord{Superseded: []SegmentID{"a"}, Cohort: []SegmentID{"b"}}, []byte{3, 0, 0, 0})

	// TRUNCATED PAST THE MAGIC. Treating this as a record-less blob would hand the
	// format an envelope header to decode as an index, which is a corrupt read
	// dressed up as a compatible one.
	_, _, err := decodeSupersession(good[:12])
	require.Error(t, err, "a blob carrying the magic but a truncated header must be REFUSED")

	// A PAYLOAD OFFSET PAST THE END.
	bad := append([]byte(nil), good...)
	bad[supMagicLen] = 0xFF
	bad[supMagicLen+1] = 0xFF
	_, _, err = decodeSupersession(bad)
	require.Error(t, err, "a payload offset outside the blob must be REFUSED")

	// CONTROL: the unmodified blob still decodes, so the two legs above fail on the
	// damage rather than on the shape.
	_, _, err = decodeSupersession(good)
	require.NoError(t, err)
}

// storedBlobBytes is the bytes a stored .seg file holds for rec and payload: the
// envelope followed by the payload.
//
// PRODUCTION NEVER CONCATENATES THESE, and that is the whole point of the split —
// the writer places the two parts into the file in sequence, so no output-sized
// buffer is ever assembled on the heap. This helper exists only so a test can
// assert on the stored SHAPE, which is still envelope-then-payload.
func storedBlobBytes(rec supersessionRecord, payload []byte) []byte {
	return append(encodeSupersessionPrefix(rec), payload...)
}
