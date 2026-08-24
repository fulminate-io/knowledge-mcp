// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// newTestSyncWatermarkStore roots a store at a per-test temp path, so no test touches the
// real ~/.knowledge/sync_watermarks.json.
func newTestSyncWatermarkStore(t *testing.T) *syncWatermarkStore {
	t.Helper()
	return &syncWatermarkStore{path: filepath.Join(t.TempDir(), "nested", "sync_watermarks.json")}
}

// TestSyncWatermarkStoreRoundTrip covers the store's whole read surface: a token survives
// a write, a missing file reads empty, and an absent key reads empty.
func TestSyncWatermarkStoreRoundTrip(t *testing.T) {
	s := newTestSyncWatermarkStore(t)

	// Missing file — the state before any pull has ever run.
	require.Empty(t, s.Load("knowledge", "default"),
		"a missing file must read as no watermark, which sends nothing and gets a full export")

	require.NoError(t, s.Save("knowledge", "default", "cg1:7:2:1:0"))
	require.Equal(t, "cg1:7:2:1:0", s.Load("knowledge", "default"), "a stored token must round-trip")

	// Absent key, with the file present and non-empty — distinct from the missing-file
	// case above, and the reason both are asserted.
	require.Empty(t, s.Load("code", "somerepo"), "an absent key must read as no watermark")

	// The key is per (graph_type, name): two graphs do not share a slot.
	require.NoError(t, s.Save("code", "somerepo", "cg1:1:1:1:1"))
	require.Equal(t, "cg1:7:2:1:0", s.Load("knowledge", "default"),
		"saving one graph's token must not disturb another's")
	require.Equal(t, "cg1:1:1:1:1", s.Load("code", "somerepo"))

	// A malformed file degrades to no watermark rather than erroring a pull.
	require.NoError(t, os.WriteFile(s.path, []byte("{not json"), 0o600))
	require.Empty(t, s.Load("knowledge", "default"), "a corrupt file must read as no watermark")

	// A nil store is tolerated by both methods.
	var nilStore *syncWatermarkStore
	require.Empty(t, nilStore.Load("knowledge", "default"))
	require.NoError(t, nilStore.Save("knowledge", "default", "cg1:9:0:0:0"))
}

// TestSyncWatermarkStoreEmptyTokenDeletesKey is the named catcher for storing the
// server's "cannot answer" signal as if it were a token. An empty token must REMOVE the
// key, so the next pull sends nothing and receives a full export.
func TestSyncWatermarkStoreEmptyTokenDeletesKey(t *testing.T) {
	s := newTestSyncWatermarkStore(t)

	// KNOWN-POSITIVE first: the key really is present, so the emptiness asserted below
	// is a deletion rather than a key that was never written.
	require.NoError(t, s.Save("knowledge", "default", "cg1:7:2:1:0"))
	require.Equal(t, "cg1:7:2:1:0", s.Load("knowledge", "default"))

	require.NoError(t, s.Save("knowledge", "default", ""))
	require.Empty(t, s.Load("knowledge", "default"), "an empty token must delete the key")

	// And the key is genuinely GONE from the file, not stored as an empty string — a
	// Load-level assertion alone cannot tell those apart, because both read as "".
	entries, err := readLocalJSONMap(s.path)
	require.NoError(t, err)
	_, present := entries[syncWatermarkKey("knowledge", "default")]
	require.False(t, present,
		"the key must be absent from the file, not present with an empty value")

	// A sibling key is untouched by the delete.
	require.NoError(t, s.Save("code", "somerepo", "cg1:1:1:1:1"))
	require.NoError(t, s.Save("knowledge", "default", ""))
	require.Equal(t, "cg1:1:1:1:1", s.Load("code", "somerepo"))
}
