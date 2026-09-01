// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// TestPut_RefusesBytesThatDoNotHashToTheirID is the write-time invariant's
// red-first proof: the producer defect this guards is a buffer that changed
// between being hashed and being written, and before this check that write
// succeeded silently.
func TestPut_RefusesBytesThatDoNotHashToTheirID(t *testing.T) {
	root := t.TempDir()
	c := newDiskSegmentCache(root, 0, adviceRandom)

	payload := []byte("the bytes that were hashed")
	id := sha256Hex(payload)

	// The honest write lands.
	require.NoError(t, c.Put(id, nil, payload))
	require.FileExists(t, filepath.Join(root, id+".seg"))

	// THE DEFECT, REPRODUCED: the same id, different bytes — a producer that
	// hashed and then mutated. It must be refused, and the error must name both
	// the id it claimed and the hash the bytes actually have, because a caller
	// reading the log needs to know which of the two is the stale one.
	mutated := []byte("the bytes that were WRITTEN")
	staleID := sha256Hex([]byte("some other segment entirely"))
	err := c.Put(staleID, nil, mutated)
	require.Error(t, err, "a write whose bytes do not hash to their id must be refused")
	require.Contains(t, err.Error(), staleID, "the error names the claimed id")
	require.Contains(t, err.Error(), sha256Hex(mutated), "the error names the hash the bytes actually have")

	// AND NOTHING REACHED DISK. A refusal that still wrote the file would be a
	// louder version of the same corruption.
	require.NoFileExists(t, filepath.Join(root, staleID+".seg"),
		"a refused write must leave no file behind")
}

// TestPut_AcceptsAnEnvelopedWriteWhoseIDNamesThePayload pins the span the
// invariant hashes, and it is the assertion that keeps this check from breaking
// every real write.
//
// THE ID NAMES THE PAYLOAD, NOT THE STORED FILE. A segment carrying a
// supersession envelope is stored as envelope-then-payload while its id is the
// hash of the payload alone, so an invariant that hashed everything handed to
// Put would refuse every enveloped segment in the store — a check that fails
// closed on correct data is worse than no check.
func TestPut_AcceptsAnEnvelopedWriteWhoseIDNamesThePayload(t *testing.T) {
	root := t.TempDir()
	c := newDiskSegmentCache(root, 0, adviceRandom)

	payload := []byte("payload bytes the id is taken from")
	id := sha256Hex(payload)
	envelope := []byte("ENVELOPE-PREFIX")

	require.NoError(t, c.Put(id, envelope, payload),
		"an enveloped write whose id names the payload must be accepted")

	stored, err := os.ReadFile(filepath.Join(root, id+".seg"))
	require.NoError(t, err)
	require.Equal(t, append(append([]byte{}, envelope...), payload...), stored,
		"the stored file is envelope followed by payload")
	require.NotEqual(t, id, sha256Hex(stored),
		"the whole stored file deliberately does NOT hash to the id — that is the distinction this test exists for")
}

// TestIncidentArtifacts_ContentAddressingIsIntact is the correction of record.
//
// The two preserved segments from the incident were first read as hash-vs-name
// MISMATCHES, and that reading is what pointed the investigation at a producer
// writing stale-id bytes. It is wrong: it hashed the whole stored file, and the
// id names the payload. Hashed correctly, both files verify.
//
// THIS TEST EXISTS SO THE CORRECTION CANNOT BE LOST. The mistaken reading is
// reproducible and superficially convincing — it is exactly what `shasum` on the
// file prints — so without an executable statement of the right span, the next
// person to check will reach the same wrong conclusion. It also pins the real
// consequence: the write-time invariant PASSES these files, so it is not the
// defense against this incident and must not be described as one.
func TestIncidentArtifacts_ContentAddressingIsIntact(t *testing.T) {
	cases := []struct {
		file string
		id   searchengine.SegmentID
	}{
		{"incident-fbc34f-enveloped.seg", "fbc34f956c49385e2040fbc740209c8aeffcfccaa55dc8b2a1d1957d3a1dfc26"},
		{"incident-955f20-enveloped.seg", "955f207c2f6c7c66c8cc15ea4e7160437c509fe96135a51b20cc8de78a2ca7e0"},
		{"incident-915fb7-unenveloped.seg", "915fb7a75690124ab694f43b8046b25eb71b3dff63bde0e661fb82a08e1c7b94"},
	}
	for _, tc := range cases {
		t.Run(tc.file, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("testdata", tc.file))
			require.NoError(t, err)

			_, payload, err := searchengine.SplitStoredBlob(raw)
			require.NoError(t, err)
			require.Equal(t, tc.id, sha256Hex(payload),
				"the PAYLOAD hashes to the filename: this segment's content address is intact")

			// The write-time invariant accepts it, which is the point: hashing
			// cannot see this incident's damage, because the damage is inside a
			// payload that hashes exactly as it should.
			require.NoError(t, verifyContentAddress(tc.id, raw),
				"the content-address check passes this file — it is not the defense against this incident")
		})
	}
}

// TestIncidentArtifacts_WholeFileHashIsTheMistakenReading pins the mistake
// itself, so a future reader can see WHY the wrong conclusion was reached rather
// than only that it was.
//
// The enveloped files differ whole-vs-payload; the third does not, because its
// envelope is empty — which is precisely why it looked like the "healthy" one in
// the original sweep and made the pattern seem conclusive.
func TestIncidentArtifacts_WholeFileHashIsTheMistakenReading(t *testing.T) {
	// THE FILENAMES SAY WHAT THESE FILES ARE, not what they were first thought to
	// be. They were originally saved as "mismatched" on the reading this test
	// disproves; naming a fixture after a conclusion that turned out to be wrong
	// is how the wrong conclusion outlives its correction.
	enveloped := []string{"incident-fbc34f-enveloped.seg", "incident-955f20-enveloped.seg"}
	for _, f := range enveloped {
		raw, err := os.ReadFile(filepath.Join("testdata", f))
		require.NoError(t, err)
		env, payload, err := searchengine.SplitStoredBlob(raw)
		require.NoError(t, err)
		require.NotEmpty(t, env, "%s carries an envelope; that is what made the whole-file hash differ", f)
		require.NotEqual(t, sha256Hex(raw), sha256Hex(payload),
			"%s: whole-file and payload hashes differ, which is the whole mechanism of the mistaken reading", f)
	}

	raw, err := os.ReadFile(filepath.Join("testdata", "incident-915fb7-unenveloped.seg"))
	require.NoError(t, err)
	env, payload, err := searchengine.SplitStoredBlob(raw)
	require.NoError(t, err)
	require.Empty(t, env, "the file that appeared healthy has NO envelope")
	require.Equal(t, sha256Hex(raw), sha256Hex(payload),
		"with no envelope the two hashes coincide, which is the only reason this file passed the mistaken check")
}

// BenchmarkVerifyContentAddress measures what the write-time invariant costs, at
// the segment sizes this store actually holds.
//
// THE SIZES ARE THE REAL DISTRIBUTION, not round numbers: a live graph's segment
// directory during this work held blobs of 2.8KB, 12KB, 17KB, 33KB, 57KB, 63KB,
// 64KB, 78KB, 111KB and 121KB, so 16KB and 128KB bracket the bulk of it. 1MB and
// 8MB are included to show the slope for a merged L2 segment far larger than
// anything observed.
func BenchmarkVerifyContentAddress(b *testing.B) {
	for _, size := range []int{16 << 10, 128 << 10, 1 << 20, 8 << 20} {
		payload := make([]byte, size)
		for i := range payload {
			payload[i] = byte(i)
		}
		id := sha256Hex(payload)
		b.Run(fmt.Sprintf("%dKiB", size>>10), func(b *testing.B) {
			b.SetBytes(int64(size))
			b.ReportAllocs()
			for b.Loop() {
				if err := verifyContentAddress(id, nil, payload); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// TestQuarantineRefusesAnUnattributedID reproduces F5: an empty id was a silent
// success.
//
// The merge path's boundary cannot say which constituent raised, so it reported
// an unattributed corruption and the owner called Quarantine(""). That composed
// <root>/.seg, stat'd it, missed, and returned NIL — reporting that a segment
// had been withdrawn when nothing had. Nothing was quarantined, the same
// constituents were re-selected on the next 50ms merge tick, and the whole k-way
// drain repeated about twenty times a second with a log line each and no
// disposition ever taken. A silent success is what let that run forever.
func TestQuarantineRefusesAnUnattributedID(t *testing.T) {
	dir := t.TempDir()
	c := newDiskSegmentCache(dir, 0, adviceRandom)

	err := c.Quarantine("", errors.New("merge boundary could not attribute the raise"))
	require.Error(t, err,
		"quarantining a nameless segment withdraws nothing, and reporting success for it is what let the merge spin forever")
	require.ErrorContains(t, err, "unattributed")

	// AND IT DID NOT INVENT A FILE. The old path stat'd <root>/.seg; nothing
	// should have been created or moved by a call that names no segment.
	entries, readErr := os.ReadDir(dir)
	require.NoError(t, readErr)
	require.Empty(t, entries, "a refused quarantine must leave the cache directory untouched")
}

// TestQuarantineRefusesAnIDTheStoreNeverKnew is the sibling of the empty-id case,
// and it hid behind the same silent success.
//
// THE CLASS IS DAMAGE IN PLACE AFTER A CORRECT WRITE. Put verifies the content
// address; Get and GetMapped do not. So a file corrupted on disk after it was
// written correctly keeps its original FILENAME — the name the index, the
// published set and every reader are keyed on — while its bytes now hash to
// something else entirely. Attribution-at-raise names the segment's TRUE hash,
// which is the honest answer to "which bytes are wrong" and the wrong key for
// "which file do I withdraw". Quarantine was then handed an id the store had
// never seen: nothing to stat, an index drop that hit nothing, and nil returned.
// Measured end-to-end at eight reports, eight reported successes, nothing
// withdrawn, the damaged file still serving.
//
// THE KNOWN-POSITIVE ARM is the idempotent case this branch exists for — a
// SECOND quarantine of an id the store does know, whose file is already moved.
// That one must still succeed, or the fix would break the concurrency tolerance
// the function is built around.
func TestQuarantineRefusesAnIDTheStoreNeverKnew(t *testing.T) {
	dir := t.TempDir()
	c := newDiskSegmentCache(dir, 0, adviceRandom)

	// SEEDED UNDER ITS TRUE HASH, because Put verifies the content address — a
	// made-up name is refused at the write, which is layer 2 doing its job and
	// also the reason this fixture cannot take the shortcut.
	payload := []byte("a correctly written segment")
	sum := sha256.Sum256(payload)
	stored := hex.EncodeToString(sum[:])
	require.NoError(t, c.Put(stored, payload), "seed a segment the store knows")

	t.Run("an id the index never held is refused, not reported as withdrawn", func(t *testing.T) {
		// The id a raise carries when the bytes were damaged in place: the true
		// hash of the damaged content, which is not the filename anything is keyed
		// on.
		const trueHashOfDamagedBytes = "2222222222222222222222222222222222222222222222222222222222222222"
		err := c.Quarantine(trueHashOfDamagedBytes, errors.New("posting run past the blob"))
		require.Error(t, err,
			"quarantining an id this store has no file and no index entry for withdraws nothing, and reporting success for it "+
				"leaves the damaged bytes in service while the engine believes they were dealt with")
		require.ErrorContains(t, err, "neither a file nor an index entry")
	})

	t.Run("KNOWN-POSITIVE: a known id whose file is already gone still succeeds", func(t *testing.T) {
		// The idempotent case: the engine reports a corruption from every
		// concurrent query, so the second caller finds the file already moved. That
		// must stay a success, or the fix breaks the concurrency tolerance this
		// function is built around.
		require.NoError(t, c.Quarantine(stored, errors.New("first withdrawal")))
		require.NoError(t, c.Quarantine(stored, errors.New("second, concurrent report")),
			"a repeat quarantine of a known id is the idempotent path and must not be refused")
	})
}
