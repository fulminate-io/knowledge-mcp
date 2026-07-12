// SPDX-License-Identifier: Apache-2.0

package transcriptsync

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// cacheRootDir is the root directory the persistent per-session parquet cache is
// written under. Empty (the production default) means
// ~/.knowledge/transcripts-cache; a test overrides it to a t.TempDir() so it can
// assert the cache-write lifecycle without touching the real home dir. Mirrors the
// tempParquetDir gochecknoglobals seam.
//
//nolint:gochecknoglobals // test-only cache-root seam; mirrors the tempParquetDir seam.
var cacheRootDir string

// cacheSessionParquet copies the just-converted temp parquet at srcPath to a stable
// per-session cache path ~/.knowledge/transcripts-cache/{source}/{session}.parquet
// so the daemon-local analyzer can query historical sessions long after the temp is
// shipped and unlinked. It reuses the temp parquet bytes already on disk (no second
// serialization) and lands the copy ATOMICALLY (temp-file + rename in the
// destination dir, mirroring WatermarkStore.writeAtomicLocked) so a concurrent
// reader never observes a half-written file.
//
// BEST-EFFORT contract: the caller (prepareFile) logs and swallows any returned
// error and continues shipping — a cache-write failure must NEVER abort a session's
// upload. The error is returned for LOGGING only.
func cacheSessionParquet(source, session, srcPath string) error {
	root := cacheRootDir
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return fmt.Errorf("transcriptsync: resolve home dir for parquet cache: %w", err)
		}
		root = filepath.Join(home, ".knowledge", "transcripts-cache")
	}
	dir := filepath.Join(root, source)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("transcriptsync: mkdir parquet cache %q: %w", dir, err)
	}
	return copyFileAtomic(srcPath, filepath.Join(dir, session+".parquet"), dir)
}

// copyFileAtomic copies srcPath to dstPath via a temp file in dstDir followed by an
// os.Rename, so dstPath either holds the complete prior copy or the complete new one
// — never a partial write. The temp file is removed on any failure before the rename
// consumes it.
func copyFileAtomic(srcPath, dstPath, dstDir string) error {
	src, err := os.Open(srcPath) //nolint:gosec // srcPath is our own just-written temp parquet, not user text.
	if err != nil {
		return fmt.Errorf("transcriptsync: open temp parquet for cache: %w", err)
	}
	defer func() { _ = src.Close() }()

	tmp, err := os.CreateTemp(dstDir, "transcripts-cache-*.parquet.tmp")
	if err != nil {
		return fmt.Errorf("transcriptsync: create temp cache file: %w", err)
	}
	tmpName := tmp.Name()
	// Best-effort cleanup if we bail before the rename consumes the temp file.
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := io.Copy(tmp, src); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("transcriptsync: copy parquet into cache: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("transcriptsync: close temp cache file: %w", err)
	}
	if err := os.Rename(tmpName, dstPath); err != nil {
		return fmt.Errorf("transcriptsync: rename temp cache into place: %w", err)
	}
	return nil
}
