// SPDX-License-Identifier: Apache-2.0

// graph_cache_drop.go — whole-graph L2 cache teardown for manage(drop_graph).
//
// PruneCache reclaims ORPHANED .seg files inside a graph that is still live; this
// file answers the opposite question — the graph is GONE server-side, so every
// local artifact rooted at it must go too, including the rebuild-state record
// whose watermark now describes blobs that no longer exist.
//
// The format set is ENUMERATED from disk (one ReadDir of the cache root) rather
// than hard-coded: hnsw, bm25 and rebuildstate are siblings under the root by
// graphCacheDirFor's layout, and a format added later must be swept without
// editing this file.

package segmentdist

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// DropGraphCacheReport describes what DropGraphCache actually removed, so the
// caller can report the truth instead of a count it inferred.
type DropGraphCacheReport struct {
	// Formats names the format directories a graph directory was removed from, in
	// os.ReadDir order (e.g. bm25, hnsw, rebuildstate). A format holding nothing
	// for this graph contributes no entry.
	Formats []string
	// Files is the number of regular files removed across every format.
	Files int
	// Bytes is their summed size.
	Bytes int64
}

// DropGraphCache removes one graph's entire L2 footprint: the per-graph directory
// under EVERY format directory found at the cache root, plus the rebuild-state
// record (which lives under its own reserved format dir and is therefore swept by
// the same enumeration).
//
// The graph directory match is EXACT — graphCacheDirFor joins the sanitized name
// as a single path element, and nothing here walks or prefix-matches. That is
// load-bearing: a cache root routinely holds sibling directories like
// code/knowledge beside code/knowledge@some-branch, and a branch overlay or a
// quarantine sibling must survive a drop of the base graph.
//
// Absence is never an error. A Manager with no durable cache, a cache root that
// was never created, and a graph never loaded locally all report a clean zero —
// the caller renders that as "no local artifacts", not as a failure. A per-format
// removal failure is accumulated and the sweep CONTINUES, so a partial cleanup
// still reports what it did remove alongside the joined error.
func (m *Manager) DropGraphCache(gt kgtypes.GraphType, name string) (DropGraphCacheReport, error) {
	var report DropGraphCacheReport
	if m.cacheDir == "" {
		return report, nil
	}

	entries, err := os.ReadDir(m.cacheDir)
	if err != nil {
		if os.IsNotExist(err) {
			return report, nil
		}
		return report, err
	}

	var errs []error
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		format := entry.Name()
		dir := graphCacheDirFor(m.cacheDir, gt, name, format)

		files, bytes, ok, merr := measureCacheDir(dir)
		if merr != nil {
			errs = append(errs, merr)
			continue
		}
		if !ok {
			continue // this format holds nothing for this graph
		}
		if rerr := os.RemoveAll(dir); rerr != nil {
			errs = append(errs, rerr)
			continue
		}
		report.Formats = append(report.Formats, format)
		report.Files += files
		report.Bytes += bytes
	}
	return report, errors.Join(errs...)
}

// measureCacheDir counts the regular files under dir and sums their sizes. ok is
// false when dir does not exist, which is the ordinary case for a format that
// never held this graph.
func measureCacheDir(dir string) (files int, bytes int64, ok bool, err error) {
	walkErr := filepath.WalkDir(dir, func(_ string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			return ierr
		}
		files++
		bytes += info.Size()
		return nil
	})
	if walkErr != nil {
		if os.IsNotExist(walkErr) {
			return 0, 0, false, nil
		}
		return 0, 0, false, walkErr
	}
	return files, bytes, true, nil
}
