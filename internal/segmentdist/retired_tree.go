// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
)

// retiredBM25Tree is the L2 cache tree the version-carrying format name left
// behind. The format name is the FIRST path component of a graph's cache
// directory, so renaming the format relocates the whole tree: everything under
// this name is never read or written again.
const retiredBM25Tree = "bm25"

// retiredTreeMarker records that the BM25 removal already ran, so it happens
// once and never re-scans. It lives beside the retired tree rather than inside
// it, since the tree itself is what gets deleted. Its HNSW sibling is
// retiredHNSWMarker, and the two are deliberately distinct — see
// removeRetiredTree for why sharing one marker would strand a tree.
const retiredTreeMarker = ".bm25-retired"

// retiredHNSWTree is the L2 cache tree the hnswv3 format name left behind, the
// HNSW counterpart to retiredBM25Tree.
const retiredHNSWTree = "hnsw"

// retiredHNSWMarker records that the HNSW removal already ran. It is a separate
// path from retiredTreeMarker on purpose: see removeRetiredTree.
const retiredHNSWMarker = ".hnsw-retired"

// removeRetiredTree deletes a superseded cache tree, once, for one format.
//
// EVERY FORMAT MUST PASS ITS OWN marker. The guard returns early when the marker
// exists, so a marker shared between formats would let whichever format
// constructed first write it and permanently suppress the other format's
// reclaim — the other tree stranded on disk with nothing saying why.
//
// THE PRECONDITION IS EXACTLY THIS: the replacement directory EXISTS. That is
// all the guard can cheaply observe, and it means at least one segment has been
// cached in the new family — ONE BLOB, not a rebuilt corpus. The stronger
// reading is tempting and would be false, so nothing here should be read as
// "the new tree is complete".
//
// What the guard is really for is narrow and worth stating: it avoids deleting
// the old tree during the window when the new one has produced nothing at all.
// It does NOT make a downgrade free. A user who downgrades after this has no old
// tree and rebuilds from the node graph; a user who downgrades before it still
// has one, but the new client has already rebuilt into the new family, so the
// downgraded client rebuilds the moment its own coverage check runs. Either way
// a downgrade means a rebuild.
//
// Failures are logged, not returned. This is disk hygiene for bytes nothing will
// ever read again — a cache tree that outlives its format costs disk and nothing
// else — so a failure to reclaim it must not fail the manager construction that
// a working search depends on.
func removeRetiredTree(base, retiredName, replacementName, marker string) {
	if base == "" {
		return
	}
	markerPath := filepath.Join(base, marker)
	if _, err := os.Stat(markerPath); err == nil {
		return // already done; do not re-scan
	} else if !errors.Is(err, os.ErrNotExist) {
		return
	}

	retired := filepath.Join(base, retiredName)
	if _, err := os.Stat(retired); errors.Is(err, os.ErrNotExist) {
		return // nothing to reclaim
	} else if err != nil {
		return
	}
	// The replacement must have produced something first.
	if _, err := os.Stat(filepath.Join(base, replacementName)); err != nil {
		return
	}

	if err := os.RemoveAll(retired); err != nil {
		slog.Warn("segmentdist: could not reclaim a retired cache tree",
			"path", retired, "err", err)
		return
	}
	if err := os.WriteFile(markerPath, []byte(retiredName+"\n"), 0o600); err != nil {
		slog.Warn("segmentdist: reclaimed a retired cache tree but could not write its marker; "+
			"the next start will re-check an absent directory",
			"marker", markerPath, "err", err)
	}
	slog.Info("segmentdist: reclaimed the retired cache tree left by the format-name change",
		"path", retired)
}
