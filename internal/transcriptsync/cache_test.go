// SPDX-License-Identifier: Apache-2.0

package transcriptsync

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCacheSessionParquet_Lifecycle proves the four cache-lifecycle guarantees over
// the cacheRootDir + tempParquetDir seams: (1) a convert writes the cache file with
// the SAME bytes as the shipped temp parquet; (2) an unchanged re-run (watermark
// hit) does NOT rewrite it; (3) --seed forces a rewrite; (4) DryRun writes no cache
// file at all.
func TestCacheSessionParquet_Lifecycle(t *testing.T) {
	tempParquetDir = t.TempDir()
	cacheRootDir = t.TempDir()
	t.Cleanup(func() { tempParquetDir = ""; cacheRootDir = "" })

	dir := t.TempDir()
	path := writeOffsetsFile(t, dir, "s.jsonl", 1, 2, 3)
	wm, _ := newTempWatermarkStore(t)
	file := TranscriptFile{Path: path, Source: "src", Session: "S"}
	cfg := Config{Parse: offsetsParse, Watermarks: wm}
	cachePath := filepath.Join(cacheRootDir, "src", "S.parquet")

	// (1) A convert writes the cache file with the temp parquet's exact bytes.
	plan, err := prepareFile(cfg, file)
	require.NoError(t, err)
	require.Positive(t, plan.emitted, "the session converted")
	require.NotEmpty(t, plan.object.path, "the temp parquet still ships")
	tempBytes, err := os.ReadFile(plan.object.path)
	require.NoError(t, err)
	cacheBytes, err := os.ReadFile(cachePath)
	require.NoError(t, err, "the cache file exists after a convert")
	assert.Equal(t, tempBytes, cacheBytes, "cache holds the same bytes as the shipped temp parquet")

	// Advance the watermark to the file's live identity AND the current rollup schema
	// version, mirroring the real post-ship cursor, so the next run is a genuine hit
	// (unchanged size/mtime AND same-schema → no version-triggered reship).
	st, err := os.Stat(path)
	require.NoError(t, err)
	require.NoError(t, wm.Advance("src:S", Watermark{Size: st.Size(), Mtime: st.ModTime().UnixNano(), RollupSchemaVersion: rollupSchemaVersion}))
	firstInfo, err := os.Stat(cachePath)
	require.NoError(t, err)

	// (2) An unchanged re-run (watermark hit) does NOT rewrite the cache file.
	plan2, err := prepareFile(cfg, file)
	require.NoError(t, err)
	require.Zero(t, plan2.emitted, "an unchanged session is skipped (no convert)")
	secondInfo, err := os.Stat(cachePath)
	require.NoError(t, err)
	assert.Equal(t, firstInfo.ModTime(), secondInfo.ModTime(),
		"the unchanged re-run left the cache file untouched")

	// (3) --seed forces a full convert = a cache rewrite (proven by its reappearance).
	require.NoError(t, os.Remove(cachePath))
	seedCfg := cfg
	seedCfg.Seed = true
	plan3, err := prepareFile(seedCfg, file)
	require.NoError(t, err)
	require.Positive(t, plan3.emitted, "--seed forces a full convert")
	_, err = os.Stat(cachePath)
	require.NoError(t, err, "--seed rewrote the cache file")

	// (4) DryRun writes NO cache file (it short-circuits before the convert).
	require.NoError(t, os.Remove(cachePath))
	dryCfg := cfg
	dryCfg.Seed = true
	dryCfg.DryRun = true
	plan4, err := prepareFile(dryCfg, file)
	require.NoError(t, err)
	require.Positive(t, plan4.emitted, "DryRun still reports the row count")
	_, statErr := os.Stat(cachePath)
	assert.True(t, os.IsNotExist(statErr), "DryRun writes no cache file")
}

// TestCacheSessionParquet_BestEffort is the T3-3 guarantee: when the cache write
// fails (here forced via an ENOTDIR cache root — a regular file where a directory is
// expected, which is root-safe and does not depend on filesystem permissions),
// prepareFile logs and CONTINUES: it returns no error and the session's temp parquet
// is still produced and will ship. The cache write never perturbs the ship flow.
func TestCacheSessionParquet_BestEffort(t *testing.T) {
	tempParquetDir = t.TempDir()
	// Point the cache root beneath a regular file so os.MkdirAll returns ENOTDIR.
	notDir := filepath.Join(t.TempDir(), "iamafile")
	require.NoError(t, os.WriteFile(notDir, []byte("x"), 0o600))
	cacheRootDir = filepath.Join(notDir, "cache")
	t.Cleanup(func() { tempParquetDir = ""; cacheRootDir = "" })

	// The helper itself surfaces the failure (returned for LOGGING only).
	require.Error(t, cacheSessionParquet("src", "S", notDir),
		"a cache write into an ENOTDIR root fails")

	dir := t.TempDir()
	path := writeOffsetsFile(t, dir, "s.jsonl", 1, 2, 3)
	wm, _ := newTempWatermarkStore(t)
	file := TranscriptFile{Path: path, Source: "src", Session: "S"}
	cfg := Config{Parse: offsetsParse, Watermarks: wm}

	plan, err := prepareFile(cfg, file)
	require.NoError(t, err, "a cache-write failure never propagates out of prepareFile")
	require.Positive(t, plan.emitted, "the session still converted")
	require.NotEmpty(t, plan.object.path, "the temp parquet is still produced")
	_, statErr := os.Stat(plan.object.path)
	require.NoError(t, statErr, "the temp parquet exists on disk (ship flow unperturbed)")
}
