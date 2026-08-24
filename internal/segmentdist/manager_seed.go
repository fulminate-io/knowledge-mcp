// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"fmt"
	"log/slog"
	"runtime"
	"strings"
	"sync"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// ensureShippedSeeded lazily seeds shippedIDs from the server's current segment
// set (Source.List(0)) so a fresh process does not re-ship the entire corpus on
// the first ship(). The server is the source of truth; the client re-derives.
// Backed by the idempotent server Put, this seed is an optimization (avoid the
// upload), not a correctness requirement.
//
// RE-ARM ON FAILURE: the seed latches (m.seeded=true) ONLY when List(0) SUCCEEDS.
// A transient List failure returns the error WITHOUT latching, so the next ship
// re-attempts the seed. This replaces the prior sync.Once+seedErr, which consumed
// the Once on the first attempt even when it failed and then returned the cached
// error forever — a single transient List failure permanently disabled shipping
// for the process lifetime. The whole seed runs under shipMu so concurrent ships
// serialize on it (the second waiter sees seeded==true and returns immediately);
// holding the lock across the List RPC is acceptable because seeding is a rare
// once-per-process success and ship() acquires shipMu only after this returns.
//
// CRITICAL: the server keys blobs by graphKey ONLY (no format dimension), so
// List(0) returns BOTH this graph's HNSW and BM25 blobs. shippedIDs must hold
// ONLY THIS engine's format ids — exactly the same keepFormat filter load()
// applies. Seeding a foreign-format id here would make reconcilePrune treat it as
// "shipped but no longer Exported" (this engine never Exports the other format)
// and PRUNE the other format's live segments server-side: e.g. the BM25 ship
// would prune the just-shipped HNSW segments, leaving VectorByID with nothing to
// resolve. The format filter is the fix for that cross-format prune.
func (m *distManager[Q, S]) ensureShippedSeeded(ctx context.Context) error {
	m.shipMu.Lock()
	defer m.shipMu.Unlock()
	if m.seeded {
		return nil
	}
	metas, err := m.source.List(ctx, 0)
	if err != nil {
		// Transient failure — do NOT latch. The next ship re-arms the seed.
		return err
	}
	for _, meta := range metas {
		if !m.keepFormat(meta.Format) {
			continue
		}
		m.shippedIDs[meta.ID] = struct{}{}
	}
	m.seeded = true
	return nil
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
// THE SOURCE SET IS WHAT THE BASE PUBLISHES, resolved through the base graph's own
// source rather than by listing its cache directory. An L2 directory can
// legitimately hold superseded blobs no manifest references, and copying one
// RESURRECTS documents the base already retired.
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
	baseSource := m.newSegmentSource(baseCache, gt, baseName, graphSelector(gt, baseName), format)
	metas, err := baseSource.List(ctx, 0)
	if err != nil {
		return nil, fmt.Errorf("segmentdist: seed %s/%s from %s: list base partitions: %w",
			gt, branchName, baseName, err)
	}
	// The same format filter the ship seed applies, and for the same reason: the
	// server keys blobs by graph alone, so a List returns both formats' ids and
	// copying a foreign-format id into this bucket would make it claim partitions
	// this engine never exports.
	published := make([]searchengine.SegmentMeta, 0, len(metas))
	ids := make([]searchengine.SegmentID, 0, len(metas))
	for _, meta := range metas {
		if meta.Format != "" && meta.Format != format {
			continue
		}
		published = append(published, meta)
		ids = append(ids, meta.ID)
	}
	if len(ids) == 0 {
		return nil, nil
	}
	if err := checkSeedFitsBudget(baseCache, ids, m.maxBytes, gt, branchName); err != nil {
		return nil, err
	}
	copied := copyPartitions(baseCache, branchCache, ids)
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

// seedShipAndPublish makes a seeded branch bucket real on the CLOUD rail: the
// copied partitions are shipped under the BRANCH's own object key and only then
// published.
//
// WHY THE UPLOAD IS UNAVOIDABLE. Blob object paths carry the graph NAME, so
// byte-identical content under a branch name is a DIFFERENT object: base's blobs
// are simply not reachable under the branch key, and the publish's server-side
// verify rejects every seeded digest until the bytes exist there. One full-corpus
// upload per branch creation is the accepted price of never streaming that corpus
// back DOWN and never rebuilding its indexes.
//
// IT IS INERT ON THE OSS RAIL BY CONSTRUCTION, not by a flavor branch: the local
// source's Ship stamps ids with zero network and its PublishManifest is a no-op,
// so this runs and costs nothing there.
//
// PUBLISH IS CONDITIONAL ON A COMPLETE SHIP, and this is the ordering that
// matters most in the whole step. A per-blob upload failure is fail-safe but not
// silent — the id is OMITTED from the returned metas rather than vanishing
// quietly — so publishing whatever came back would declare a bucket complete
// while it is missing documents, which is exactly what the downstream
// shipped-complete gate would then believe. An incomplete ship therefore fails
// loudly and publishes NOTHING. A crash between the two is the safe intermediate:
// blobs exist, nothing claims completeness, and the branch keeps reading through
// the two-pool union until a later pass publishes.
//
// It never runs on a read path and never holds the manager mutex.
//
// IT READS THE BYTES BACK THROUGH THE CALLER'S CACHE — the same instance the
// copy wrote into, and the same one the branch's engine reads. Constructing a
// private instance here would work only by accident: it would have to re-scan
// the directory to see the copy, which is exactly the second-instance coupling
// the seed no longer has.
func (m *Manager) seedShipAndPublish(
	ctx context.Context, gt kgtypes.GraphType, branchName, format string,
	seeded []searchengine.SegmentMeta, branchCache *diskSegmentCache,
) error {
	if len(seeded) == 0 {
		return nil
	}
	branchSource := m.newSegmentSource(branchCache, gt, branchName, graphSelector(gt, branchName), format)

	blobs := make([]*knowledgev1.SegmentBlobProto, 0, len(seeded))
	var bytesShipped int64
	for _, meta := range seeded {
		body, ok := branchCache.Get(meta.ID)
		if !ok {
			continue
		}
		bytesShipped += int64(len(body))
		blobs = append(blobs, &knowledgev1.SegmentBlobProto{
			Id:       meta.ID,
			Format:   format,
			Bytes:    body,
			DocCount: int32(meta.DocCount), //nolint:gosec // a per-segment doc count cannot exceed int32
		})
	}
	shipped, err := branchSource.Ship(ctx, blobs)
	if err != nil {
		return fmt.Errorf("segmentdist: seed ship %s/%s: %w", gt, branchName, err)
	}
	confirmed := make(map[searchengine.SegmentID]struct{}, len(shipped))
	for _, meta := range shipped {
		confirmed[meta.GetId()] = struct{}{}
	}
	if !seedShipComplete(seeded, confirmed) {
		return fmt.Errorf(
			"segmentdist: seed ship %s/%s confirmed %d of %d partitions — refusing to publish a bucket that "+
				"would read complete while missing %d",
			gt, branchName, len(confirmed), len(seeded), len(seeded)-len(confirmed))
	}
	digests := make([]segmentDigest, 0, len(seeded))
	for _, meta := range seeded {
		digests = append(digests, segmentDigest{ID: meta.ID, DocCount: meta.DocCount})
	}
	if _, err := branchSource.PublishManifest(format, digests); err != nil {
		return fmt.Errorf("segmentdist: seed publish %s/%s (%d digests): %w", gt, branchName, len(digests), err)
	}
	// The accepted cost of the ruling, made an OBSERVED number rather than an
	// assumed one. This line is the only evidence anyone will have that the trade
	// is behaving as it was costed.
	slog.Info("segmentdist: shipped and published a seeded branch bucket",
		"graph_type", gt, "branch", branchName, "format", format,
		"partitions", len(digests), "bytes_shipped", bytesShipped)
	return nil
}

// seedShipComplete reports whether EVERY seeded partition was confirmed by the
// ship. It is a separate predicate rather than an inline length compare because
// a length compare is the wrong test: the ship can confirm an id that was not
// seeded, and two sets of equal size are not the same set.
func seedShipComplete(seeded []searchengine.SegmentMeta, confirmed map[searchengine.SegmentID]struct{}) bool {
	for _, meta := range seeded {
		if _, ok := confirmed[meta.ID]; !ok {
			return false
		}
	}
	return true
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
// The base's published set can legitimately name a partition this machine's cache
// does not hold, and the rebuild axis fetches whatever the branch ends up missing.
// The COUNT is what makes a short seed visible; the caller reports it.
func copyPartitions(src *diskSegmentCache, dst *diskSegmentCache, ids []searchengine.SegmentID) int {
	workers := max(min(runtime.NumCPU(), len(ids)), 1)
	work := make(chan searchengine.SegmentID, len(ids))
	for _, id := range ids {
		work <- id
	}
	close(work)

	// A mutex around the shared counter rather than a results channel: the writes
	// are rare (one per partition) and a mutex is cheaper than a channel send for
	// that shape.
	var (
		mu     sync.Mutex
		copied int
	)
	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			for id := range work {
				b, ok := src.Get(id)
				if !ok {
					continue
				}
				dst.Put(id, b)
				mu.Lock()
				copied++
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	return copied
}
