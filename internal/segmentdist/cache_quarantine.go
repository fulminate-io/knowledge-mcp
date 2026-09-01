// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// cache_quarantine.go — what the owner does when the engine reports that a
// stored segment's bytes are unreadable.
//
// THE SEGMENT IS MOVED, NOT DELETED, and that is deliberate on two counts. The
// file is EVIDENCE: it is the only artifact that can explain how a
// content-addressed store came to hold bytes that do not hash to their own name,
// and the incident that motivated this was diagnosed entirely from two preserved
// files. And a delete is unrecoverable if the diagnosis is wrong, whereas a
// rename is undone with a mv.
//
// QUARANTINING COSTS THE DOCUMENTS THAT SEGMENT HELD, AND THIS FILE SAYS SO.
// An earlier version of this header claimed the opposite — that the server holds
// an authoritative copy and a quarantined id is "simply a local miss: the next
// read re-fetches it". That is false in this tree. There is no fetch path;
// diskSegmentCache.Put's own doc records that this cache IS the segment store
// and that there is nothing to re-Fetch from, which is why a swallowed write
// error there is called silent data loss. Believing the old claim makes
// quarantining look free, and it is not.
//
// WHAT IS ACTUALLY TRUE. Withdrawing the segment is still the right move —
// serving bytes a reader cannot parse is worse than serving fewer documents, and
// the alternative is a process that dies on every query touching it. But the
// documents in that segment are UNREACHABLE until the graph's segments are
// rebuilt from its nodes, so the withdrawal is a loss to be reported and acted
// on rather than a free repair. The log line at the end of Quarantine names that
// rebuild, and names the impact.

// quarantineDirName is the subdirectory under a graph's segment root that holds
// segments withdrawn from service. scanExisting skips directories and anything
// without a .seg extension at the root level, so a file moved in here is not
// re-adopted on the next start — which is what makes the withdrawal survive a
// restart rather than resuming the crash loop.
const quarantineDirName = "quarantine"

// Quarantine withdraws a corrupt segment from service: the .seg file is renamed
// into the quarantine subdirectory and the in-memory index entry is dropped, so
// no later load re-adopts it and this process stops serving those bytes.
//
// IT IS IDEMPOTENT AND CONCURRENCY-TOLERANT BY CONSTRUCTION. The engine reports
// a corruption from EVERY concurrent query that touches the segment, so this is
// called repeatedly for the same id and the second caller finds the file already
// moved. That is a no-op, not an error: the index entry is still dropped so this
// process stops serving the id either way.
//
// A FAILED RENAME STILL DROPS THE INDEX ENTRY. Leaving a known-corrupt id
// resident because the filesystem refused a move would keep handing the engine
// bytes it has already reported as unreadable; the file is left in place, named
// loudly in the log, and the error is returned.
func (c *diskSegmentCache) Quarantine(id searchengine.SegmentID, reason error) error {
	// AN EMPTY ID IS REFUSED LOUDLY RATHER THAN STAT'D. Without this the call
	// composed <root>/.seg, missed, and returned nil — reporting success for a
	// quarantine that withdrew nothing. The caller that produced it was the merge
	// path, whose boundary cannot say which constituent raised, so it reported an
	// unattributed corruption; nothing was ever quarantined, the same merge was
	// re-selected on the next 50ms tick, and the whole k-way drain plus scratch
	// write repeated about twenty times a second with a log line each. A silent
	// success is what let that run forever.
	if id == "" {
		return fmt.Errorf(
			"segmentdist: refusing to quarantine an unattributed corruption (%v): no segment was named, so there is nothing to withdraw — "+
				"the caller must attribute the raise (searchengine.RaiseCorruptIn) or leave the disposition to the store census", reason)
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	src := c.path(id)
	if _, statErr := os.Stat(src); statErr != nil {
		// THE SIBLING OF THE EMPTY-ID BUG, and it hid the same way. A stat miss on
		// a KNOWN id is the idempotent case this function is built for — a second
		// caller finding the file already moved — so dropping the index entry and
		// returning nil is right there. But an id the index NEVER HELD is a
		// different fact wearing the same clothes: nothing is moved, the drop is a
		// no-op, and a nil return reports a withdrawal that did not happen.
		//
		// IT IS REACHED BY DAMAGE-IN-PLACE. Get and GetMapped do not verify the
		// content address — only Put does — so a file corrupted on disk AFTER a
		// correct write keeps its original FILENAME while its bytes now hash to
		// something else. The raise attributes to the segment's TRUE hash, which is
		// not the name anything is keyed on, so this branch is handed an id the
		// store has never seen. Measured end-to-end: eight reports, eight
		// successes, nothing withdrawn, the damaged file still published.
		// ALREADY WITHDRAWN IS NOT UNKNOWN, and the quarantine directory is the
		// record that distinguishes them without any new state. The engine reports
		// a corruption from EVERY concurrent query touching the segment, so a
		// second caller routinely arrives after the file has been moved and the
		// index entry dropped — at which point the id is absent from the index too,
		// and a naive "does the index know it" check would refuse the idempotent
		// path this function is built around. The moved file under quarantine/ is
		// proof this store did withdraw that id, so this call is a repeat and
		// succeeds.
		if _, movedErr := os.Stat(filepath.Join(c.root, quarantineDirName, id+".seg")); movedErr == nil {
			c.dropIndexLocked(id)
			return nil
		}
		_, known := c.index[id]
		if !known {
			return fmt.Errorf(
				"segmentdist: refusing to quarantine segment %s (%v): this cache holds neither a file nor an index entry under that id, "+
					"so nothing was withdrawn — the reported id is not the name this store keys on, which happens when a file was damaged "+
					"in place after a correct write and the raise named the bytes' true hash instead of the filename", id, reason)
		}
		c.dropIndexLocked(id)
		return nil
	}

	dir := filepath.Join(c.root, quarantineDirName)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		c.dropIndexLocked(id)
		slog.Error("segmentdist: CORRUPT SEGMENT withdrawn from service but could not be moved aside",
			"segment", id, "file", src, "graph_dir", c.root, "invariant", reason, "error", err)
		return fmt.Errorf("segmentdist: create quarantine dir for segment %s: %w", id, err)
	}

	dst := filepath.Join(dir, id+".seg")
	if err := os.Rename(src, dst); err != nil {
		c.dropIndexLocked(id)
		slog.Error("segmentdist: CORRUPT SEGMENT withdrawn from service but could not be moved aside",
			"segment", id, "file", src, "graph_dir", c.root, "invariant", reason, "error", err)
		return fmt.Errorf("segmentdist: quarantine segment %s: %w", id, err)
	}
	c.dropIndexLocked(id)

	// ERROR, not WARN. The store is content-addressed: a file whose bytes do not
	// match its name means something wrote bytes it had already hashed, and that
	// is a defect in this process rather than an environmental hiccup. It names
	// the file, the graph directory it came from, and the invariant that was
	// violated, so the reader can act without reproducing anything.
	// THE RECOVERY LINE NAMES A REBUILD, NOT A FETCH, and the correction matters
	// more than its length suggests. An earlier version of this log promised the
	// segment "re-fetches from the server on the next read". There is no such
	// path in this tree: Put's own doc records that this cache IS the segment
	// store and there is nothing to re-fetch from. An operator reading the old
	// line would wait for a repair that was never coming, which is worse than no
	// guidance at all. What actually restores these documents is a rebuild of the
	// graph's segments from its nodes.
	slog.Error("segmentdist: CORRUPT SEGMENT quarantined and withdrawn from service",
		"segment", id, "moved_to", dst, "graph_dir", c.root, "invariant", reason,
		"recovery", "rebuild this graph's segments (manage rebuild_segments) — nothing re-fetches it; the file is kept as evidence",
		"impact", "the documents this segment held are unreachable until that rebuild runs")
	return nil
}

// dropIndexLocked removes id from the in-memory index, the LRU list and the byte
// accounting WITHOUT touching the filesystem. Caller holds c.mu.
//
// It is deliberately NOT removeElement: that unlinks the .seg file, which would
// destroy the evidence this path exists to preserve.
func (c *diskSegmentCache) dropIndexLocked(id searchengine.SegmentID) {
	el, ok := c.index[id]
	if !ok {
		return
	}
	if ent, entOK := el.Value.(*cacheEntry); entOK {
		c.curByt -= ent.bytes
	}
	c.ll.Remove(el)
	delete(c.index, id)
}
