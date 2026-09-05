package searchengine

import (
	"fmt"
	"os"
	"path/filepath"
)

// mergeEntry consolidates segs into a published-ready entry, and owns the merge
// file's WHOLE lifecycle while it does: create, write, size, map, unlink, decode,
// attach.
//
// THE FORMAT WRITES AND REPORTS A LENGTH; EVERYTHING ELSE HAPPENS HERE. That
// split is the point of the MergeTo interface — a format that created, sized or
// unlinked its own destination could not have its output mapped by the engine,
// and a merged payload that is not mapped is a whole-segment heap copy.
//
// THE UNLINK LANDS IMMEDIATELY AFTER THE MAP SUCCEEDS, not at the end, so no
// error path after it has to remember to clean up: past that point the file has
// no name and the mapping keeps the bytes alive on its own. Deleting a mapped
// file is safe on unix — the mapping holds the inode, not the directory entry —
// and it is the same idiom bm25's merge already used for its temp file. On
// Windows the mapping layer opens with FILE_SHARE_DELETE, which is the flag that
// permits delete-while-mapped; that arm is traced from the source rather than
// executed, and is recorded at that confidence.
//
// A MapBlob FAILURE FAILS THE MERGE. It does not read the file onto the heap
// instead. A second lane reachable on a mapping error would be a fallback — it
// would repair nothing, it could fire forever on the same cause, and it would
// silently forfeit the property this whole path exists to deliver, on a signal
// nobody is watching. The correct move is to say so and stop.
//
// ONE FUNCTION, ONE OBLIGATION. The release returned by MapBlob must have exactly
// one owner on every path: attached to the entry on success, released explicitly
// on failure. The three engine call sites each remembering that would be three
// chances to forget it, which is precisely how the mapping leak this package
// already documents came back the first time.
func (e *SegmentedIndex[Q, S]) mergeEntry(segs []Segment[Q, S], accept []func(ExternalID) bool) (*segmentEntry[Q, S], error) {
	f, err := e.openMergeScratch()
	if err != nil {
		return nil, err
	}
	path := f.Name()
	// The name stays claimed until this function returns, by which point the file
	// has been unlinked on every path. A sibling merge's sweep therefore cannot
	// remove it while it is being written.
	defer e.forgetMergeScratch(path)
	// Until the map succeeds, this function owns the file on every path.
	mapped := false
	defer func() {
		if !mapped {
			_ = f.Close()
			_ = os.Remove(path)
		}
	}()

	// THE MERGE READS EVERY CONSTITUENT'S PER-DOCUMENT DATA, so it reaches the
	// same accessors a query does and can raise the same corruption — hnsw's
	// MergeTo walks rangeVectors, resolving each node's id and vector. Raised on
	// the merger's own goroutine with nothing above it, that ends the process.
	//
	// THE ID IS EMPTY DELIBERATELY: a merge drains several inputs interleaved and
	// this boundary cannot say which one raised. Naming the wrong constituent
	// would be worse than naming none — the owner would quarantine a healthy
	// segment. The census is what identifies the bad file; this boundary's job is
	// to keep the daemon alive and fail the merge.
	var n int64
	if err := e.containCorrupt("", func() error {
		var mergeErr error
		n, mergeErr = e.format.MergeTo(f, segs, accept)
		return mergeErr
	}); err != nil {
		return nil, err
	}
	// THE TRUNCATE IS THE ENGINE'S, and it is required rather than tidy: a format
	// may leave the destination longer than the segment, because writing at an
	// aligned offset advances a writer's tail past the last byte carrying content.
	// n is what makes the file exactly the segment.
	if err := f.Truncate(n); err != nil {
		return nil, fmt.Errorf("searchengine: sizing merge scratch to %d: %w", n, err)
	}
	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("searchengine: closing merge scratch: %w", err)
	}

	data, release, err := e.opts.MapBlob(path)
	if err != nil {
		return nil, fmt.Errorf("searchengine: mapping merged segment: %w", err)
	}
	mapped = true
	if err := os.Remove(path); err != nil {
		releaseUnattached(release)
		return nil, fmt.Errorf("searchengine: unlinking merge scratch %s: %w", path, err)
	}

	// THE OUTPUT IS READ BACK UNDER THE SAME BOUNDARY THE INPUTS WERE. Decode and
	// newEntry resolve the merged artifact's own per-document data — newEntry
	// calls IDs(), which walks the member table — so an artifact this merge just
	// wrote inconsistently raises HERE, on the merger's goroutine, with nothing
	// above it. The containment above covered only MergeTo and stopped one line
	// short of the read-back, which left the crash loop alive for ENGINE-WRITTEN
	// output: the same merge is re-attempted after every restart, and the file it
	// dies on is one this process produced rather than one it was handed.
	var entry *segmentEntry[Q, S]
	if err := e.containCorrupt("", func() error {
		seg, decErr := e.format.Decode(data)
		if decErr != nil {
			return fmt.Errorf("searchengine: decoding merged segment: %w", decErr)
		}
		built, entErr := e.newEntry(seg, nil)
		if entErr != nil {
			return entErr
		}
		entry = built
		return nil
	}); err != nil {
		releaseUnattached(release)
		return nil, err
	}
	attachBlobCleanup(entry, release)
	return entry, nil
}

// openMergeScratch creates this merge's scratch file, sweeping stale ones first.
//
// THE SWEEP IS PER-MERGE AND ITS FAILURE IS THE MERGE'S ERROR. A crash between a
// scratch file's creation and its unlink leaves a file the size of a merged
// segment that nothing else ever reaps: it sits in a SUBDIRECTORY of the L2 cache
// root, and the cache's own accounting skips directories, so neither its byte
// budget nor its eviction loop can see it. Sweeping here rather than at engine
// construction is what lets a failure be reported at all — the pool constructors
// return a manager and no error, so a construction-time sweep could only have
// been logged and stepped past.
//
// "STALE" MEANS "NOT OWNED BY AN IN-FLIGHT MERGE OF THIS ENGINE", and it cannot
// mean anything simpler. Several merges share one scratch directory —
// harvestGroup runs min(NumCPU, partitions) of them concurrently — so a sweep
// that removed every file it found would delete a sibling's output mid-write.
// The claim and the creation happen under one lock so no sweep can interleave
// between them.
//
// IT NEVER SWEEPS AN UNSET ScratchDir. Empty means os.CreateTemp's own default,
// which is the system temp directory shared with every other process on the
// machine; sweeping there would delete files this engine has no claim over.
func (e *SegmentedIndex[Q, S]) openMergeScratch() (*os.File, error) {
	dir := e.opts.ScratchDir

	e.scratchMu.Lock()
	defer e.scratchMu.Unlock()

	if dir != "" {
		// os.CreateTemp does not create the directory, so an absent one would fail
		// every merge.
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, fmt.Errorf("searchengine: creating merge scratch directory %s: %w", dir, err)
		}
		if err := e.sweepStaleScratchLocked(dir); err != nil {
			return nil, err
		}
	}

	f, err := os.CreateTemp(dir, "merge-*.seg")
	if err != nil {
		return nil, fmt.Errorf("searchengine: creating merge scratch: %w", err)
	}
	if e.scratchLive == nil {
		e.scratchLive = make(map[string]bool)
	}
	e.scratchLive[filepath.Base(f.Name())] = true
	return f, nil
}

// forgetMergeScratch releases this merge's claim on its scratch name.
func (e *SegmentedIndex[Q, S]) forgetMergeScratch(path string) {
	e.scratchMu.Lock()
	delete(e.scratchLive, filepath.Base(path))
	e.scratchMu.Unlock()
}

// sweepStaleScratchLocked removes every file in dir that no in-flight merge of
// this engine owns. Caller holds scratchMu.
//
// IT REMOVES FILES ONLY, NEVER DIRECTORIES AND NEVER dir ITSELF: mergeEntry's
// os.CreateTemp needs the directory to exist, and this scope is deliberately
// narrow because the directory sits under the operator's cache root.
func (e *SegmentedIndex[Q, S]) sweepStaleScratchLocked(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("searchengine: reading merge scratch directory %s: %w", dir, err)
	}
	for _, ent := range entries {
		if ent.IsDir() || e.scratchLive[ent.Name()] {
			continue
		}
		if err := os.Remove(filepath.Join(dir, ent.Name())); err != nil {
			return fmt.Errorf("searchengine: removing stale merge scratch %s: %w", filepath.Join(dir, ent.Name()), err)
		}
	}
	return nil
}
