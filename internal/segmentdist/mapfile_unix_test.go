// SPDX-License-Identifier: Apache-2.0

//go:build unix

package segmentdist

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

// TestMapBlobFileRecordsAdviceOutcome proves the read-ahead advice is actually
// APPLIED, not merely un-errored.
//
// The distinction matters because the obvious test — map a file, assert no
// error — passes just as happily against a mapBlobFile that never calls
// Madvise at all, and an unadvised mapping silently carries about twice the
// physical footprint this seam exists to cut. So the outcome is recorded on the
// mapping at the call site that applies it, and asserted here.
//
// The known-positive for the MECHANISM is the last case: this test advises a
// mapping it made itself and requires that to succeed, which establishes that a
// recorded "true" on this platform reflects an advice that can really be
// applied rather than one that systematically fails. The complementary check —
// that the call exists in the source at all, with comments stripped so prose
// cannot satisfy it — is a separate structural gate over mapfile_unix.go.
func TestMapBlobFileRecordsAdviceOutcome(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "blob.seg")
	payload := bytes.Repeat([]byte("segment-bytes-"), 4096)
	require.NoError(t, os.WriteFile(path, payload, 0o600))

	m, err := mapBlobFile(path, adviceRandom)
	require.NoError(t, err)
	require.NotNil(t, m)
	require.True(t, m.randomAccessHinted,
		"the mapping does not record that read-ahead suppression was applied")

	// The mapping really is the file: a shim that returned a heap copy would
	// pass the flag assertion above but is not what this seam is for.
	require.Len(t, m.data, len(payload))
	require.True(t, bytes.Equal(payload, m.data), "mapped bytes differ from the file's")

	require.NoError(t, m.release())
	require.Nil(t, m.data, "release must clear the slice so a later read cannot touch unmapped pages")
	require.NoError(t, m.release(), "release must be safe to call twice")

	// Error paths yield no mapping at all rather than an unadvised one.
	missing, err := mapBlobFile(filepath.Join(dir, "does-not-exist.seg"), adviceRandom)
	require.Error(t, err)
	require.Nil(t, missing)

	empty := filepath.Join(dir, "empty.seg")
	require.NoError(t, os.WriteFile(empty, nil, 0o600))
	zero, err := mapBlobFile(empty, adviceRandom)
	require.Error(t, err, "a zero-length blob has nothing to map and must not be reported as mapped")
	require.Nil(t, zero)

	// KNOWN-POSITIVE for the advice mechanism itself.
	f, err := os.Open(path) //nolint:gosec // test-owned temp path
	require.NoError(t, err)
	data, err := unix.Mmap(int(f.Fd()), 0, len(payload), unix.PROT_READ, unix.MAP_SHARED)
	require.NoError(t, err)
	require.NoError(t, f.Close())
	require.NoError(t, unix.Madvise(data, unix.MADV_RANDOM),
		"MADV_RANDOM fails on this platform, so a recorded success would be meaningless")
	require.NoError(t, unix.Munmap(data))
}

// TestMappedBlobSurvivesDescriptorClose pins the property that lets hundreds of
// segments be resident at once: mapBlobFile closes the descriptor as soon as the
// mapping exists, and the mapping stays fully readable afterwards. Without it
// every resident segment would hold a descriptor against a soft limit in the
// hundreds.
func TestMappedBlobSurvivesDescriptorClose(t *testing.T) {
	dir := t.TempDir()
	payload := bytes.Repeat([]byte("abcdefgh"), 8192)

	var maps []*mappedBlob
	t.Cleanup(func() {
		for _, m := range maps {
			_ = m.release()
		}
	})
	// Comfortably past the soft descriptor limit this machine reports, so a
	// shim that held its descriptors open would fail here rather than in
	// production.
	for i := range 400 {
		path := filepath.Join(dir, "blob"+string(rune('a'+i%26))+string(rune('a'+i/26))+".seg")
		require.NoError(t, os.WriteFile(path, payload, 0o600))
		m, err := mapBlobFile(path, adviceRandom)
		require.NoError(t, err, "mapping %d failed — a held descriptor would exhaust the limit here", i)
		maps = append(maps, m)
	}
	require.Len(t, maps, 400)
	for i, m := range maps {
		require.True(t, bytes.Equal(payload, m.data), "mapping %d became unreadable after its descriptor closed", i)
	}
}
