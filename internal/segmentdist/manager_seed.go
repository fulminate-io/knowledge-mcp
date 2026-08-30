// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"fmt"
	"log/slog"
	"runtime"
	"strings"
	"sync"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/bm25"
)

// baseLiveSetForFormat returns base's LIVE segment ids for one format: the ids its
// engine CURRENTLY exports.
//
// IT DELIBERATELY DOES NOT FORCE A LOAD, and that is the whole correctness argument.
// Every available "load the base" primitive — load, loadResidentFromL2,
// forceCompleteLiveSet — imports cache.Keys(), which is the L2 DIRECTORY LISTING. A
// directory can legitimately hold superseded blobs no live layer references (the
// merge-reclaim window, and every retired layer between a swap and the next
// InvalidateLocal), so forcing a load would import those retired blobs into the base
// engine and hand them straight back as "live". That is the exact resurrection this
// function's caller documents as the hazard to avoid.
//
// The engine's in-memory layer is the ONLY thing that knows which of the blobs on
// disk are live, because ReplaceLayer retires a layer in memory while its files
// linger until a reclaim. So:
//
//   - a WARM base engine exports its current layer — the live, non-superseded set,
//     which is what a seed wants;
//   - a COLD base engine exports nothing, the seed copies nothing, and the branch
//     rebuilds from its own embedded nodes. That is slower and CORRECT; the
//     alternative is a branch serving documents its base already retired.
func (m *Manager) baseLiveSetForFormat(
	gt kgtypes.GraphType, baseName, format string,
) []searchengine.SegmentID {
	var exported []searchengine.SegmentBlob
	if format == bm25.New().Name() {
		exported = m.bm25ManagerFor(gt, baseName).engine.Export()
	} else {
		exported = m.managerFor(gt, baseName).engine.Export()
	}
	ids := make([]searchengine.SegmentID, 0, len(exported))
	for _, b := range exported {
		ids = append(ids, b.ID)
	}
	return ids
}

// SeedBranchBucketFromBase copies the base graph's published segment partitions
// into a BRANCH graph's own L2 bucket, and returns the partitions that landed.
//
// WHY A COPY AT ALL. A branch graph is a new graph name with an empty segment
// bucket, so without this the rebuild axis pages the branch's entire vector
// corpus down the wire and rebuilds HNSW and BM25 from scratch — for content that
// is byte-identical to base's and already correct, merely behind the branch head.
// Seeding from base makes the rebuild axis stream only what actually differs, and
// the touched-partitions-only replace path absorbs that difference.
//
// THE SOURCE SET IS BASE'S LIVE SET — the ids its ENGINE exports — never a listing
// of its cache directory. An L2 directory can legitimately hold superseded blobs no
// live layer references, and copying one RESURRECTS documents the base already
// retired.
//
// THAT DISTINCTION USED TO BE FREE AND NOW HAS TO BE MADE ON PURPOSE. The set came
// from base's segment source, which was a remote manifest read — inherently the
// published, non-superseded set. The only source left is the L2 cache itself, whose
// List IS the directory listing this paragraph warns against, so the operand is
// taken from the engine instead: baseLiveSetForFormat reads base's CURRENT Export
// without forcing a load, which is the live set after the engine has applied
// whatever supersession its layers encode. Its doc explains why every
// force-a-load primitive is disqualified here.
//
// IT COPIES THROUGH THE CACHE'S OWN PUT PATH, never by copying files. The cache
// owns its byte-budget accounting and its LRU, and a filesystem copy would leave
// both wrong — a bucket whose accounting understates its contents evicts at the
// wrong time and silently drops partitions the manifest still names.
//
// THE CEILING IS THE BRANCH BUCKET'S BYTE BUDGET, and exceeding it FAILS LOUDLY
// naming the graph and the shortfall rather than copying a prefix. This is not
// defensive framing: the cache evicts LRU on Put, so an over-budget copy would
// silently evict the partitions it copied first and leave a bucket that reads as
// seeded while missing documents — exactly what a shipped-complete gate would
// then believe.
//
// CRASH WINDOW. The copy writes only into the branch's own bucket and destroys
// nothing on either side. A crash part-way leaves a PARTIAL branch bucket, which
// is why this stamps no completeness marker of its own: the caller latches only
// on a nil error, and an interrupted seed re-runs from scratch — the key space is
// content-addressed, so re-copying is idempotent.
//
// IT RETURNS THE PARTITIONS IT COPIED, not just a count, because on the cloud
// rail the copy is only half the seed: the blobs must then exist under the
// BRANCH's own object key before anything can publish them, and the ship needs
// each partition's identity and doc count to do that.
//
// THE DESTINATION IS THE CALLER'S CACHE — the very instance the branch's engine
// reads — and passing it in rather than constructing one here is what makes the
// seed take effect in the process that ran it. A diskSegmentCache builds its
// in-memory index once, at construction, and Keys() never re-reads the
// directory; a private second instance over the same directory therefore leaves
// the engine's index empty, and the engine imports nothing until a restart
// rebuilds it. The copy already writes through the cache's own Put, whose index
// maintenance takes the same mutex Get and Keys take, so with the right instance
// the copied partitions are visible the moment they land.
func (m *Manager) SeedBranchBucketFromBase(
	ctx context.Context, gt kgtypes.GraphType, baseName, branchName, format string, branchCache *diskSegmentCache,
) ([]searchengine.SegmentMeta, error) {
	if !isBranchGraphName(branchName) || baseName == "" || baseName == branchName {
		return nil, nil
	}
	// A bucket that already holds partitions is either already seeded or already
	// carrying the branch's own work. Either way base's content is not what it
	// needs, and copying into it would be the resurrection hazard above.
	// Asking the ENGINE's instance makes this guard report what the engine
	// actually holds rather than what some other view of the directory holds.
	if len(branchCache.Keys()) > 0 {
		return nil, nil
	}

	// BASE'S REBUILD RECORD IS CAPTURED FIRST, BEFORE A SINGLE PARTITION MOVES, and
	// the ordering is the whole crash argument rather than a style choice.
	//
	// Reading it AFTER the copy admits a window in which base publishes more in
	// between, which would give the branch a watermark NEWER than the blobs it
	// actually holds — and a watermark past a document the branch never received
	// means that document is never scanned again and is permanently missing, with
	// no error anywhere. Capturing first can only OVER-copy: the branch's watermark
	// is then older than its blobs, which costs one re-emit and loses nothing.
	//
	// A crash between the capture and the record write leaves partitions and no
	// record, which loads as a zero watermark — the full rebuild, and the safe
	// direction.
	baseWatermark, baseTombstoned, err := m.LoadRebuildState(gt, baseName)
	if err != nil {
		return nil, fmt.Errorf("segmentdist: seed %s/%s: read base rebuild record: %w", gt, branchName, err)
	}
	// TEST-ONLY hook, placed HERE because the ordering above is what it exists to
	// check: the record has been captured and not one partition has moved, so a test
	// advancing base's record from this phase reproduces exactly the window a
	// capture-after-copy implementation would lose data in. nil in production.
	if m.seedHook != nil {
		m.seedHook(seedPhaseRecordCaptured, gt, branchName, format)
	}

	// THE BASE SIDE IS READ-ONLY, AND MUST STAY THAT WAY. This is a second cache
	// instance over a directory the base graph's own engine owns, so its index is
	// accurate only as of this construction. That is tolerable for List and Get —
	// staleness can only make the seed copy FEWER partitions, which the
	// copied-fewer-than-published Warn below already reports. It would NOT be
	// tolerable for a write: each instance keeps its own LRU accounting, so a Put
	// or Remove through this one would make its eviction decisions diverge from the
	// owning instance's and delete files out from under it. If a future change
	// needs to write to base's bucket, it needs base's own engine instance.
	advice, err := adviceForFormat(format)
	if err != nil {
		return nil, err
	}
	baseCache := newDiskSegmentCache(graphCacheDirFor(m.cacheDir, gt, baseName, format), m.maxBytes, advice)

	// BASE'S LIVE SET, FROM BASE'S ENGINE. baseLiveSetForFormat reads base's CURRENT
	// Export ids — the live, non-superseded set — WITHOUT forcing a load. Listing
	// baseCache instead, or forcing a load through any of the primitives that import
	// cache.Keys(), would hand back the raw directory with superseded blobs included,
	// which is the resurrection hazard this function's doc names.
	// It reads an in-memory Export and cannot fail, so it returns the ids alone.
	liveIDs := m.baseLiveSetForFormat(gt, baseName, format)
	// INTERSECTED WITH WHAT THE BASE CACHE ACTUALLY HOLDS. A live id whose bytes are
	// not on this machine cannot be copied, and reporting it as copyable would
	// overstate the seed; the copied-fewer Warn below is what surfaces the gap.
	published := make([]searchengine.SegmentMeta, 0, len(liveIDs))
	ids := make([]searchengine.SegmentID, 0, len(liveIDs))
	for _, id := range liveIDs {
		if _, present := baseCache.sizeOf(id); !present {
			continue
		}
		published = append(published, searchengine.SegmentMeta{ID: id, Format: format})
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return nil, nil
	}
	if err := checkSeedFitsBudget(baseCache, ids, m.maxBytes, gt, branchName); err != nil {
		return nil, err
	}
	copied, err := copyPartitions(baseCache, branchCache, ids)
	if err != nil {
		return nil, fmt.Errorf("segmentdist: seed %s/%s from %s: copy base partitions: %w",
			gt, branchName, baseName, err)
	}
	if copied < len(ids) {
		// The base publishes partitions this machine's cache does not hold. The
		// branch is not wrong — the rebuild axis fetches the remainder — but the
		// saving is smaller than the published set suggests, so say so rather than
		// reporting a seed that looks complete.
		slog.Warn("segmentdist: branch seed copied fewer partitions than the base publishes",
			"graph_type", gt, "base", baseName, "branch", branchName, "format", format,
			"copied", copied, "published", len(ids))
	}
	// THE RECORD IS WRITTEN FROM THE CAPTURED VALUE, never re-read from base here.
	// BOTH FIELDS RIDE IT: the branch's seeded partitions ARE base's partitions and
	// carry the same stale ids, so copying the watermark while dropping the
	// tombstones would leave the branch serving deleted ids out of those partitions
	// with nothing scheduled to remove them.
	//
	// A Manager with no cache directory keeps no durable state at all, so this is a
	// no-op there — the same answer LoadRebuildState gives — and not an error.
	if err := m.SaveRebuildState(gt, branchName, baseWatermark, baseTombstoned); err != nil {
		return nil, fmt.Errorf("segmentdist: seed %s/%s: write branch rebuild record: %w", gt, branchName, err)
	}
	slog.Info("segmentdist: seeded a branch segment bucket from its base",
		"graph_type", gt, "base", baseName, "branch", branchName, "format", format, "partitions", copied,
		"inherited_watermark_nanos", baseWatermark, "inherited_tombstones", len(baseTombstoned))
	// Only the partitions that actually landed in the bucket are reported, so a
	// caller shipping them cannot ship an id whose bytes are not there.
	resident := make([]searchengine.SegmentMeta, 0, copied)
	for _, meta := range published {
		if _, ok := branchCache.Get(meta.ID); ok {
			resident = append(resident, meta)
		}
	}
	return resident, nil
}

// isBranchGraphName reports whether a graph name is branch-qualified. The '@'
// split is the same one the scope resolution uses to separate a base name from
// its branch.
func isBranchGraphName(name string) bool { return strings.Contains(name, "@") }

// checkSeedFitsBudget refuses a seed that cannot fit in the destination's byte
// budget, BEFORE any bytes are copied.
//
// IT MUST RUN FIRST. The cache evicts LRU on Put, so discovering the overflow
// during the copy is discovering it too late: the partitions copied earliest are
// already gone and the bucket looks populated. The sizes come from the source
// cache's own in-memory accounting, so this costs no disk reads.
func checkSeedFitsBudget(
	src *diskSegmentCache, ids []searchengine.SegmentID, budget int64, gt kgtypes.GraphType, branchName string,
) error {
	if budget <= 0 {
		return nil // unbounded budget: nothing to exceed
	}
	var total int64
	for _, id := range ids {
		size, ok := src.sizeOf(id)
		if !ok {
			continue
		}
		total += size
	}
	if total > budget {
		return fmt.Errorf(
			"segmentdist: seed %s/%s needs %d bytes for %d partitions but the segment cache budget is %d "+
				"(short by %d) — refusing to copy a prefix, which would leave the bucket reading as seeded "+
				"while missing documents",
			gt, branchName, total, len(ids), budget, total-budget)
	}
	return nil
}

// copyPartitions copies each id's bytes from src to dst with a BOUNDED worker
// pool — never one goroutine per blob, which is what an unbounded spawn over a
// large corpus becomes. The work is I/O-bound and the blobs are independent, so
// the pool is sized to the CPU count the way the ship path's own upload pool is.
//
// A MISS IS SKIPPED, NOT AN ERROR, and the cache's read seam is why it cannot be
// anything else: Get reports hit-or-miss and has no failure of its own to report.
// Base's live set can legitimately name a partition this machine's cache does not
// hold, and the rebuild axis fetches whatever the branch ends up missing. The COUNT
// is what makes a short seed visible; the caller reports it.
//
// A FAILED WRITE IS A DIFFERENT THING ENTIRELY AND ABORTS THE SEED. A miss means
// base never had the bytes here; a Put error means the bytes could not be written to
// the branch's bucket, and continuing past it produces exactly the outcome the
// budget check above refuses to produce — a bucket that reads as seeded while
// missing documents. The caller latches only on a nil error, so returning one here
// is what makes an interrupted seed re-run from scratch.
//
// THE FIRST ERROR ACROSS THE WORKERS IS THE ONE RETURNED, recorded under the same
// mutex as the counter. The remaining workers drain their queue rather than being
// cancelled: the copy is additive and idempotent (content-addressed keys), so
// finishing costs a little I/O and avoids a second failure mode in the unwind path.
func copyPartitions(src *diskSegmentCache, dst *diskSegmentCache, ids []searchengine.SegmentID) (int, error) {
	workers := max(min(runtime.NumCPU(), len(ids)), 1)
	work := make(chan searchengine.SegmentID, len(ids))
	for _, id := range ids {
		work <- id
	}
	close(work)

	// A mutex around the shared counter rather than a results channel: the writes
	// are rare (one per partition) and a mutex is cheaper than a channel send for
	// that shape. The first error rides the same mutex.
	var (
		mu      sync.Mutex
		copied  int
		firstEr error
	)
	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			for id := range work {
				b, ok := src.Get(id)
				if !ok {
					continue
				}
				err := dst.Put(id, b)
				mu.Lock()
				if err != nil {
					if firstEr == nil {
						firstEr = err
					}
				} else {
					copied++
				}
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if firstEr != nil {
		return copied, firstEr
	}
	return copied, nil
}
