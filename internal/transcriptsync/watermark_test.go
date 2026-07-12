// SPDX-License-Identifier: Apache-2.0

package transcriptsync

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestWatermarkStore_AdvanceLookupRoundTrip asserts a written watermark survives
// a process restart: a FRESH WatermarkStore over the same on-disk path reads the
// identical struct back, and the file is valid JSON keyed by source:session.
func TestWatermarkStore_AdvanceLookupRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript-watermarks.json")
	key := "claude:sess-abc"
	want := Watermark{Size: 8192, Mtime: 1730000111222333444}

	writer := &WatermarkStore{path: path}
	require.NoError(t, writer.Advance(key, want))

	// A fresh store (no shared in-memory state) reads the same struct back.
	reader := &WatermarkStore{path: path}
	got, ok := reader.Lookup(key)
	require.True(t, ok, "the advanced key is found after a fresh open")
	assert.Equal(t, want, got, "both fields round-trip identically")

	// The on-disk file is valid JSON keyed by source:session.
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var decoded map[string]Watermark
	require.NoError(t, json.Unmarshal(raw, &decoded), "manifest is valid JSON")
	_, keyed := decoded[key]
	assert.True(t, keyed, "manifest is keyed by source:session")

	// A never-written key reports ok=false (no error).
	_, missing := reader.Lookup("codex:never")
	assert.False(t, missing, "absent key reports not-found")
}

// TestWatermark_RollupSchemaVersionJSON pins the rollup_schema_version watermark field:
// it marshals under the frozen json key and round-trips, and — critically — a watermark
// JSON with NO rollup_schema_version key (a pre-deploy cursor) decodes to 0, which is the
// pre-bump value that makes the initial 0→1 schema bump re-ship every locally-held session.
func TestWatermark_RollupSchemaVersionJSON(t *testing.T) {
	raw, err := json.Marshal(Watermark{Size: 8192, Mtime: 42, RollupSchemaVersion: 3})
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"rollup_schema_version":3`, "marshals under the frozen key")

	var back Watermark
	require.NoError(t, json.Unmarshal(raw, &back))
	assert.Equal(t, 3, back.RollupSchemaVersion, "the version round-trips")

	// A legacy watermark with the key absent decodes to 0 (the pre-bump value).
	var legacy Watermark
	require.NoError(t, json.Unmarshal([]byte(`{"size":8192,"mtime":42}`), &legacy))
	assert.Equal(t, 0, legacy.RollupSchemaVersion, "absent key decodes to 0 (pre-deploy)")
}

// TestWatermarkStore_AtomicWrite_NoTempLeftover asserts the atomic write contract
// mirrored from repo_manifest: after Advance the directory holds ONLY the
// manifest — the temp file was renamed into place (os.Rename) and no *.tmp
// straggler remains, so a crash mid-write can never corrupt the cursor.
func TestWatermarkStore_AtomicWrite_NoTempLeftover(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript-watermarks.json")
	store := &WatermarkStore{path: path}

	require.NoError(t, store.Advance("claude:s1", Watermark{Size: 1, Mtime: 1}))
	require.NoError(t, store.Advance("codex:s2", Watermark{Size: 2, Mtime: 2}))

	ents, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, ents, 1, "exactly one file in the dir — the manifest, no temp leftovers")
	assert.Equal(t, "transcript-watermarks.json", ents[0].Name())
	for _, e := range ents {
		assert.False(t, strings.HasSuffix(e.Name(), ".tmp"),
			"no repos-style *.tmp straggler remains after the rename")
	}
}
