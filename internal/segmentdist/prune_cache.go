// SPDX-License-Identifier: Apache-2.0

// prune_cache.go — one-shot orphaned-L2-segment reclaim. PruneCache diffs the
// on-disk .seg ids under each graph's per-format L2 cache root against that
// graph's COMPLETE current live set (every segment id the engine would serve
// after a force-full load) and removes the orphans — the .seg files no live
// segment references, the accumulated backlog of superseded blobs that the
// invalidation-driven reclaim never unlinked.
//
// The complete-live-set diff is the entire safety story, because a too-SMALL
// live set false-prunes a live segment (data loss). Three load-bearing
// guarantees, each its own helper so the test battery exercises them in
// isolation:
//
//  1. forceCompleteLiveSet (manager.go path) resets the load floor and reloads,
//     so Export() is the COMPLETE current generation — NOT the resident-only set
//     a bare Export() returns (an unloaded-but-live segment would otherwise look
//     orphaned).
//  2. completeHNSWLiveSet UNIONs the embed (m.managers) AND deterministic
//     (m.detManagers) engines' Export ids — both share the one "hnsw" cache root
//     (graphCacheDirFor keys by format name, and hnsw.New().Name() ==
//     hnsw.NewDeterministic().Name() == "hnsw"), so a live deterministic blob
//     absent from the embed Export is STILL live on disk; missing it = data loss.
//  3. liveSetSubsetOfList0 cross-checks the computed live set against the server's
//     List(0) — if the live set is NOT a subset (a load returned an incomplete
//     view), the pool is HARD-SKIPPED (Aborted) and prunes NOTHING rather than
//     warn-and-prune.
//
// Orphan removal is a DIRECT os.Remove of <dir>/<id>.seg, NOT the index-gated
// diskSegmentCache.Remove: Remove only unlinks ids present in the cache's
// in-memory index (built once by scanExisting at construction), so an orphan that
// appeared AFTER construction is a silent no-op there. The orphans this command
// targets are by-definition not live in any engine, so a direct unlink is safe
// and complete.

package segmentdist

import (
	"context"
	"os"
	"path/filepath"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/bm25"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/hnsw"
)

// PruneCacheTarget is the segmentdist-native (graphType, name) pair PruneCache
// iterates. It is INTERNAL to segmentdist: the tools seam crosses the package
// boundary as parallel []kgtypes.GraphType + []string slices (no segmentdist
// import in tools), and the bootstrap client_segment.go adapter constructs
// []PruneCacheTarget from those slices before calling PruneCache, so the tools
// layer never names this type.
type PruneCacheTarget struct {
	GraphType kgtypes.GraphType
	Name      string
}

// PruneCacheGraphReport is the per-(graph, format) prune result. Orphans is the
// would-remove set (Execute=false) OR the did-remove set (Execute=true) of .seg
// ids; Bytes is the summed FileInfo.Size() of those .seg files. Aborted+
// AbortReason capture a List(0) subset-abort for this (graph, format) so the
// report surfaces a SKIPPED pool rather than silently pruning a graph whose
// computed live set is not a subset of the server's shipped set.
type PruneCacheGraphReport struct {
	GraphType   kgtypes.GraphType
	Name        string
	Format      string
	Orphans     []searchengine.SegmentID
	Bytes       int64
	Aborted     bool
	AbortReason string
}

// PruneCacheReport is the whole-run result: one PruneCacheGraphReport per
// (graph, format) pool, plus the EXECUTED totals (Removed count + RemovedBytes)
// summed only over .seg files actually unlinked (Execute=true and os.Remove
// succeeded). On a preview run (Execute=false) Removed/RemovedBytes are zero and
// the would-remove counts live in the per-pool Orphans/Bytes.
type PruneCacheReport struct {
	Graphs       []PruneCacheGraphReport
	Removed      int
	RemovedBytes int64
}

// forceCompleteLiveSet resets this engine's load floor and reloads so the
// subsequent Export() is the COMPLETE current generation — every segment id the
// server holds for this graph+format — NOT the resident-only set a bare Export()
// returns. It then returns those exported ids.
//
// WHY the reset+load: Export() (distribution.go) serializes only the segments
// CURRENTLY RESIDENT in the engine; a segment dropped by unloadUnderPressure
// (engine.Unload CAS-removes it) is still LIVE on the server and on disk but absent
// from a bare Export(). Diffing the on-disk ids against a resident-only Export
// would mark that unloaded-but-live segment an orphan and DELETE it — data loss.
// importedGen.Store(0) + load(ctx) (the manager_backstop.go recoverIfDegenerate
// idiom) re-Lists(0) the full corpus and re-Imports it, making Export() complete.
//
// SAFE-BY-IDEMPOTENCE (no lock, no gate): this force-load mutates a LIVE engine the
// daemon may also be serving (the reconcile loop, a lazy recover, a concurrent
// search). That is safe WITHOUT any single-flight guard because load() re-imports
// are idempotent by segment id — publishImport drops any already-resident id, never
// double-adding — and the importedGen.Store(0) reset only triggers a re-List(0) +
// re-Import of the SAME corpus, never a delete. A concurrent search that races the
// reset pays at most a wasted re-import (a cost, not a correctness issue), exactly
// as recoverIfDegenerate documents. prune-cache is a one-shot operator command, so
// the cost is paid once.
func (m *distManager[Q, S]) forceCompleteLiveSet(ctx context.Context) ([]searchengine.SegmentID, error) {
	m.importedGen.Store(0)
	if err := m.load(ctx); err != nil {
		return nil, err
	}
	exported := m.engine.Export()
	ids := make([]searchengine.SegmentID, 0, len(exported))
	for _, b := range exported {
		ids = append(ids, b.ID)
	}
	return ids, nil
}

// completeHNSWLiveSet is the COMPLETE live set for a graph's shared "hnsw" L2
// cache root: the UNION of the embed engine's (m.managers) and the deterministic
// engine's (m.detManagers) force-loaded Export ids. The union is load-bearing for
// data safety: both engines root their L2 cache under the SAME directory
// (graphCacheDirFor keys by format NAME, and hnsw.New().Name() ==
// hnsw.NewDeterministic().Name() == "hnsw"; content-hash filenames keep their
// blobs disjoint on disk), so a segment built by the DETERMINISTIC rebuild lives
// under that one root yet never appears in the EMBED engine's Export. Diffing the
// on-disk ids against the embed set alone would mark every live deterministic blob
// an orphan and delete it — data loss. Forcing BOTH engines' live sets re-imports
// each engine's corpus, which is safe-by-idempotence on a daemon-served live engine
// (load() dedups by segment id — see forceCompleteLiveSet) — no gate.
func (m *Manager) completeHNSWLiveSet(ctx context.Context, gt kgtypes.GraphType, name string) (map[searchengine.SegmentID]struct{}, error) {
	live := make(map[searchengine.SegmentID]struct{})

	// Embed engine (m.managers, hnsw.New()) — same accessor managerFor uses.
	embedIDs, err := m.managerFor(gt, name).forceCompleteLiveSet(ctx)
	if err != nil {
		return nil, err
	}
	for _, id := range embedIDs {
		live[id] = struct{}{}
	}

	// Deterministic engine (m.detManagers, hnsw.NewDeterministic()) — the rebuild
	// path's engine, sharing the one "hnsw" root. autoReclaim=false mirrors the
	// rebuild path's construction (manager_owner.go); a force-load never merges, so
	// the flag is immaterial here, but it must match so the memoized instance is the
	// SAME one the rebuild path uses.
	detIDs, err := m.hnswManagerFor(m.detManagers, hnsw.NewDeterministic(), gt, name, false).forceCompleteLiveSet(ctx)
	if err != nil {
		return nil, err
	}
	for _, id := range detIDs {
		live[id] = struct{}{}
	}

	return live, nil
}

// completeBM25LiveSet is the COMPLETE live set for a graph's "bm25" L2 cache root:
// the single BM25 engine's force-loaded Export ids. BM25 has NO determinism variant
// (the rebuild path's deterministic split is HNSW-only — manager_owner.go), so there
// is exactly one engine per graph for this root and NO union.
func (m *Manager) completeBM25LiveSet(ctx context.Context, gt kgtypes.GraphType, name string) (map[searchengine.SegmentID]struct{}, error) {
	ids, err := m.bm25ManagerFor(gt, name).forceCompleteLiveSet(ctx)
	if err != nil {
		return nil, err
	}
	live := make(map[searchengine.SegmentID]struct{}, len(ids))
	for _, id := range ids {
		live[id] = struct{}{}
	}
	return live, nil
}

// onDiskSeg is one .seg file found under an L2 cache root: its content-hash id (the
// filename without the ".seg" suffix) and its on-disk byte size.
type onDiskSeg struct {
	id    searchengine.SegmentID
	bytes int64
}

// listOnDiskSegIDs reads every <id>.seg file directly under dir and returns its id
// + FileInfo size. A missing dir (a graph that never cached a segment for this
// format) returns an empty slice and a nil error — not an error condition.
//
// This is a STANDALONE reader, deliberately NOT diskSegmentCache.scanExisting: that
// method is unexported AND populates a live in-memory LRU index we do not want to
// instantiate for a one-shot scan. Reusing it would mean constructing a live
// diskSegmentCache per (graph, format) purely to read its directory — so a tiny
// dedicated reader is the right call here. It mirrors scanExisting's .seg-suffix +
// dir-skip filter (cache.go).
func listOnDiskSegIDs(dir string) ([]onDiskSeg, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	segs := make([]onDiskSeg, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || filepath.Ext(name) != ".seg" {
			continue
		}
		info, ierr := e.Info()
		if ierr != nil {
			continue
		}
		segs = append(segs, onDiskSeg{id: name[:len(name)-len(".seg")], bytes: info.Size()})
	}
	return segs, nil
}

// liveSetSubsetOfList0 reports whether EVERY id in the computed live set is present
// in the server's authoritative List(0) manifest for THIS engine's format. It is the
// subset-abort guard: a live set that is NOT a subset of List(0) means a load
// returned an incomplete view (so the diff would false-prune), and the caller
// HARD-SKIPS the pool rather than pruning against a suspect live set.
//
// PER-FORMAT-TIGHT: the server keys blobs by graphKey only (no format dimension), so
// List(0) returns BOTH this graph's HNSW and BM25 ids. The candidate id set is
// filtered through this engine's keepFormat predicate so the HNSW live set is checked
// only against the shipped HNSW ids (and BM25 against BM25) — an unfiltered check
// would be loose (over-permissive), never falsely aborting but not tight. List(0) is
// residency-independent (the source's authoritative manifest), so it is the right
// reference regardless of what is currently resident.
func (m *distManager[Q, S]) liveSetSubsetOfList0(ctx context.Context, live map[searchengine.SegmentID]struct{}) (bool, error) {
	metas, err := m.source.List(ctx, 0)
	if err != nil {
		return false, err
	}
	shipped := make(map[searchengine.SegmentID]struct{}, len(metas))
	for _, meta := range metas {
		if !m.keepFormat(meta.Format) {
			continue
		}
		shipped[meta.ID] = struct{}{}
	}
	for id := range live {
		if _, ok := shipped[id]; !ok {
			return false, nil
		}
	}
	return true, nil
}

// PruneCache diffs each graph's on-disk L2 .seg ids against its COMPLETE current
// live set (per format: HNSW = embed ∪ deterministic, BM25 = the single engine)
// and removes the orphans. It PREVIEWS by default: with execute=false it reports
// the would-remove orphans per (graph, format) and deletes NOTHING; with
// execute=true it unlinks each orphan via a direct os.Remove and counts only the
// successful removals.
//
// Per graph it processes the HNSW pool then the BM25 pool. For each pool: compute
// the complete live set, cross-check it against List(0) (subset-abort SKIPS the pool
// untouched on a non-subset), diff the on-disk ids to orphans, and — only when
// execute — unlink each orphan.
//
// SERIAL over graphs by design: each pool's force-full-load mutates the shared
// per-graph engine maps under m.mu (hnswManagerFor/bm25ManagerFor both Lock it) and
// is RPC-bound, so a per-graph worker pool would contend on m.mu for no real gain on
// a one-shot operator command.
func (m *Manager) PruneCache(ctx context.Context, graphs []PruneCacheTarget, execute bool) (PruneCacheReport, error) {
	var report PruneCacheReport
	for _, g := range graphs {
		// HNSW pool — embed ∪ deterministic live set, shared "hnsw" L2 root.
		hnswLive, err := m.completeHNSWLiveSet(ctx, g.GraphType, g.Name)
		if err != nil {
			return PruneCacheReport{}, err
		}
		// Re-fetch the memoized embed distManager (check-construct-store returns the
		// SAME instance) for the per-format subset-check; the det engine shares the
		// embed engine's "hnsw" format + target, so either engine's keepFormat/source
		// is equivalent — use the embed manager's.
		hnswSubset, err := m.managerFor(g.GraphType, g.Name).liveSetSubsetOfList0(ctx, hnswLive)
		if err != nil {
			return PruneCacheReport{}, err
		}
		hnswDir := graphCacheDirFor(m.cacheDir, g.GraphType, g.Name, hnsw.New().Name())
		if err := m.prunePoolReport(&report, g, hnsw.New().Name(), hnswDir, hnswLive, hnswSubset, execute); err != nil {
			return PruneCacheReport{}, err
		}

		// BM25 pool — single engine, its own "bm25" L2 root, no union.
		bm25Live, err := m.completeBM25LiveSet(ctx, g.GraphType, g.Name)
		if err != nil {
			return PruneCacheReport{}, err
		}
		bm25Subset, err := m.bm25ManagerFor(g.GraphType, g.Name).liveSetSubsetOfList0(ctx, bm25Live)
		if err != nil {
			return PruneCacheReport{}, err
		}
		bm25Dir := graphCacheDirFor(m.cacheDir, g.GraphType, g.Name, bm25.New().Name())
		if err := m.prunePoolReport(&report, g, bm25.New().Name(), bm25Dir, bm25Live, bm25Subset, execute); err != nil {
			return PruneCacheReport{}, err
		}
	}
	return report, nil
}

// prunePoolReport is the per-(graph, format) tail shared by the HNSW and BM25 pools:
// subset-abort guard, on-disk diff, and (execute only) the direct-os.Remove unlink.
// It appends exactly one PruneCacheGraphReport to report and accumulates the EXECUTED
// totals. Non-generic so both engine instantiations route through one body; the
// caller supplies the already-computed live set + subset result for the pool.
//
// The orphan unlink is a DIRECT os.Remove(filepath.Join(dir, id+".seg")) — NEVER the
// index-gated diskSegmentCache.Remove. Remove only reaches os.Remove when the id is
// in the cache's in-memory index, which scanExisting populates ONCE at construction;
// an orphan that appeared after construction (or simply was never resident) is absent
// from that index, so cache.Remove would silently no-op and leave the .seg on disk.
// An orphan is by definition not in the live set and not resident, so a direct unlink
// is the obviously-safe and complete op, and it handles the SHARED "hnsw" root
// uniformly (one .seg file per id, one os.Remove unlinks it for both engines).
func (m *Manager) prunePoolReport(
	report *PruneCacheReport,
	g PruneCacheTarget,
	format, dir string,
	live map[searchengine.SegmentID]struct{},
	subsetOK, execute bool,
) error {
	if !subsetOK {
		// Live set is NOT a subset of List(0): the load returned an incomplete view, so
		// pruning against it could false-prune. SKIP the pool untouched.
		report.Graphs = append(report.Graphs, PruneCacheGraphReport{
			GraphType:   g.GraphType,
			Name:        g.Name,
			Format:      format,
			Aborted:     true,
			AbortReason: "live set not a subset of List(0) — incomplete, skipping",
		})
		return nil
	}

	onDisk, err := listOnDiskSegIDs(dir)
	if err != nil {
		return err
	}

	pool := PruneCacheGraphReport{GraphType: g.GraphType, Name: g.Name, Format: format}
	for _, seg := range onDisk {
		if _, isLive := live[seg.id]; isLive {
			continue
		}
		pool.Orphans = append(pool.Orphans, seg.id)
		pool.Bytes += seg.bytes
		if execute {
			// Direct unconditional unlink — NOT cache.Remove (see method doc).
			if rerr := os.Remove(filepath.Join(dir, seg.id+".seg")); rerr != nil {
				// Surface the failure in the pool report; do NOT count it as removed.
				pool.AbortReason = "partial: os.Remove failed for " + seg.id + ": " + rerr.Error()
				continue
			}
			report.Removed++
			report.RemovedBytes += seg.bytes
		}
	}
	report.Graphs = append(report.Graphs, pool)
	return nil
}
