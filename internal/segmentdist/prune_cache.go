// SPDX-License-Identifier: Apache-2.0

// prune_cache.go — one-shot orphaned-L2-segment reclaim. PruneCache diffs the
// on-disk .seg ids under each graph's per-format L2 cache root against that
// graph's COMPLETE current live set (every segment id the engine would serve
// after a force-full load) and removes the orphans.
//
// WHAT IT REAPS IS WHAT THE STORED CORPUS LETS IT CLASSIFY, AND THAT IS MEASURED
// RATHER THAN ARGUED. The live set is force-loaded FROM the pool's own L2 index
// (forceCompleteLiveSet -> loadResidentFromL2 -> cache.Keys()), so an id that index
// holds is in the set the diff is taken against UNLESS the import declines it. Two
// classes are therefore reaped:
//
//  1. a .seg file the pool's in-memory index does not know about — one that appeared
//     after scanExisting built that index, or was never Put through this cache
//     instance;
//  2. a stored blob another stored blob RECORDS as superseded — the pre-merge
//     constituent an aborted reclaim strands, now that a consolidated blob names what
//     it replaced and the import declines it (searchengine/supersession.go).
//
// THE SECOND CLASS DID NOT USED TO EXIST, and its absence was the defect: a blob Put
// through this cache and never Removed was in the index, hence in the live set, hence
// never an orphan, however thoroughly it had been superseded. The corpus recorded an id
// and a payload and nothing that said "superseded by", so a prune could not classify
// what it could not read. The measurements are
// TestPruneCacheLiveSetExcludesWhatTheStoredBlobsSupersede and
// TestPruneCacheReapsAnUnreclaimedMergeConstituent (prune_cache_reclaim_abort_test.go).
//
// WHAT IS STILL NOT REAPED HERE is everything no stored record names: a refused layer's
// already-written blobs (manager_publish_resident.go, manager_rebuild_finalize.go) and
// the retired BM25 set InvalidateLocal is not fed (manager_rebuild_entry.go). Those
// blobs were never published as a consolidation's constituents, so nothing on disk
// supersedes them, and they stay until something supersedes them by content. Each of
// those sites used to promise this command would reap its leftovers; each now names
// this paragraph instead.
//
// A SEARCH REAPS THE SAME SECOND CLASS SOONER, and by a different route: an aborted
// reclaim's obligation is retained and discharged on the next consumer touch
// (manager_reclaim_discharge.go), which unlinks the constituents without waiting for a
// prune. The two are independent — the discharge needs the process that aborted, the
// prune needs only the stored record.
//
// NARROWING THE LIVE SET IS EMPHATICALLY NOT THAT FIX, and the same test file
// measures why: in the ordinary write-before-first-search order a process's engine
// holds only the segment it just wrote while L2 holds the whole prior corpus, so an
// engine-resident-only live set condemns every stored blob the process did not build.
// That is the wipe the five signatures in restart_falseprune_test.go exist to catch,
// reached through the write path instead of the read path
// (TestPruneCacheColdStartStillLoadsTheWholeCorpus).
//
// The complete-live-set diff is the entire safety story, because a too-SMALL
// live set false-prunes a live segment (data loss). Three load-bearing
// guarantees, each its own helper so the test battery exercises them in
// isolation:
//
//  1. forceCompleteLiveSet re-imports the current L2 set, so Export() is the
//     COMPLETE live set — NOT the resident-only set a bare Export() returns (an
//     unloaded-but-live segment would otherwise look orphaned).
//  2. completeHNSWLiveSet reads the ONE HNSW engine's Export ids. It used to UNION a
//     second, deterministic engine's set, because the rebuild's blobs landed under this
//     same cache root yet never appeared in the embed Export — and missing them meant
//     deleting live data. The reset finalizes at this engine now, so its blobs are in
//     this Export by construction and there is no second set to union.
//  3. prunePoolReport REFUSES a pool whose live set is EMPTY while the pool holds
//     .seg files, and reports the refusal. That is the floor case of the same
//     property: a live set can be too small by one segment or by all of them, and
//     the all-of-them case is a whole-pool wipe.
//
// A FOURTH GUARANTEE USED TO SIT HERE and is gone with the rail: a cross-check of the
// computed live set against the source's List(0) manifest. It cannot be restated
// locally — List(0) is now the L2 cache the live set was loaded from, so the check
// would compare the cache against itself and pass unconditionally. That reasoning is
// correct AND it is the same reasoning as the reap paragraph above: the check was
// dropped for being cache-against-cache, and the live-set diff it guarded is
// cache-against-cache for exactly the same reason. What was lost with the rail was not
// one check but the only second authority there was.
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
	"strconv"

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
// ids; Bytes is the summed FileInfo.Size() of those .seg files.
//
// Aborted+AbortReason capture a REFUSED pool so the report surfaces a pool that was
// deliberately left untouched rather than one that had nothing to prune. The two are
// indistinguishable without it — both show zero orphans — which is why the refusal
// below reports through this pair rather than simply returning.
//
// THE PAIR IS RETAINED RATHER THAN RENAMED. It was introduced for the List(0)
// subset-abort, which is gone; it now carries the empty-live-set refusal. Keeping the
// existing shape means every consumer that already reads Aborted keeps working, where
// a named successor would have required updating each of them to learn nothing new.
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

// forceCompleteLiveSet reloads this engine from L2 so the subsequent Export() is the
// COMPLETE stored corpus for this graph+format — every segment id the L2 CACHE holds
// — NOT the resident-only set a bare Export() returns. It then returns those exported
// ids.
//
// THIS FUNCTION IS THE GUARD AGAINST THE WIPE, and that is its reason for existing
// rather than an incidental benefit. PruneCache decides what to unlink by DIFFING the
// on-disk .seg ids against a live set. If that live set is ever the resident-only
// view, everything not currently loaded is an orphan by construction and an executing
// prune DELETES THE WHOLE CORPUS. A fresh process is the worst case: its engine has
// loaded nothing, so a bare Export() is EMPTY and the diff condemns every segment on
// disk. Nothing else on the prune path re-checks this, so weakening or short-
// circuiting the reload below is sufficient on its own to destroy a corpus.
//
// The signature tests for that hazard are TestFreshProcessCannotRetireAPriorCorpus
// and its four siblings in restart_falseprune_test.go. They are the reworked form of
// the cloud rail's false-prune signatures: the incident shape is identical — a
// process holding only its own tail retires a corpus it did not build — and only the
// actuator moved, from a server Prune RPC to this local live-set diff.
//
// WHY the reset+load: Export() (distribution.go) serializes only the segments
// CURRENTLY RESIDENT in the engine; a segment that engine.Unload CAS-removed is
// still LIVE on disk but absent from a bare Export(). Diffing the on-disk ids
// against a resident-only Export would mark that unloaded-but-live segment an
// orphan and DELETE it — data loss.
// loadResidentFromL2 imports cache.Keys() and does NOT consult the l2Loaded
// once-guard, so it always re-imports the CURRENT L2 set and Export() is then the
// complete live set. It is called directly rather than through load(), which is
// L2-PRIMARY and short-circuits on the l2Loaded once-guard (already set this
// process): load() would no-op the second call and leave the unloaded-but-live
// segment out of the live set — a false-prune/data-loss.
//
// IT MUTATES A LIVE ENGINE, AND THE COST OF THAT IS NOT ONLY A COST. The force-load
// runs against an engine the daemon may also be serving, on the PREVIEW run as much
// as the executing one — execute is not consulted until after the live set exists.
// No single-flight guard is needed, because the re-imports are idempotent by segment
// id: publishImport drops any already-resident id and never double-adds, so a
// re-import of the SAME corpus is wasted work and nothing more.
//
// BUT THE IMPORTED SET IS NOT THE SERVING SET. It is the whole L2 index, which can be
// a strict SUPERSET of what the engine currently serves — and importing that superset
// re-publishes segments the engine had already superseded. When one of them predates a
// delete, the deleted document becomes searchable again: a correctness consequence,
// not a wasted re-import. TestPruneCacheResurrectsTheDeletedID measures it on the
// PREVIEW run, which is the one an operator reaches for precisely because they expect
// it to inspect rather than to change anything.
//
// THAT CONSEQUENCE IS NOT THIS COMMAND'S TO FIX, and attributing it here would send a
// fix to the wrong file. The same import runs on the ordinary read path — load()'s
// PRIMARY branch — so one plain search after such a delete resurrects the document
// with no prune in the picture at all (TestOrdinaryReadResurrectsAStrandedConstituent).
//
// A cold L2 (errL2CacheCold) is not an error here: the live set is simply empty.
// The EMPTY case is not benign downstream, though — prunePoolReport REFUSES a pool
// whose live set is empty rather than unlinking every .seg in it.
//
// IT CLEARS THE EVICTED LATCH (markMaterialized), and that is not optional. Both
// branches above re-import the pool WITHOUT going through load() — the one place
// that otherwise clears the latch — so a residency-evicted pool would come out of
// this force fully resident yet still latched evicted: manage(status) would render
// it in the evicted band, every background decider would decline it forever, its
// bytes would be missing from the residency budget's accounting, and it could never
// be evicted again. A census over load() call sites structurally cannot see this
// site, which is why it is named here.
// ctx is unread for the same reason load's is — the whole load subtree became local
// once the cloud rail was deleted. See the note on distManager.load (manager_load.go).
//
//nolint:unparam // ctx is unread since the cloud rail's deletion; see distManager.load's note — removing it cascades into segmentdist's public Manager API
func (m *distManager[Q, S]) forceCompleteLiveSet(ctx context.Context) ([]searchengine.SegmentID, error) {
	if err := m.loadResidentFromL2(); err != nil && err != errL2CacheCold {
		return nil, err
	}
	m.markMaterialized()
	exported := m.engine.Export()
	ids := make([]searchengine.SegmentID, 0, len(exported))
	for _, b := range exported {
		ids = append(ids, b.ID)
	}
	return ids, nil
}

// completeHNSWLiveSet is the COMPLETE live set for a graph's HNSW L2 cache root: the
// one HNSW engine's force-loaded Export ids.
//
// IT USED TO BE A UNION, and the union is gone because the second engine is. The rebuild
// wrote a DETERMINISTIC engine whose blobs landed under this same root (graphCacheDirFor
// keys by format NAME, which was "hnsw" for both) yet never appeared in the embed
// engine's Export — so diffing the on-disk ids against the embed set alone would have
// marked every live rebuilt blob an orphan and deleted it. That hazard cannot arise now:
// the reset finalizes at THIS engine, so a rebuilt blob is resident here by construction
// and the force-load below covers it. The OSS-path guard that kept this from
// constructing a fresh staging engine just to prune-scan the disk goes with it — there
// is no second engine to construct.
//
// The force-load re-imports the engine's corpus, which is safe-by-idempotence on a
// daemon-served live engine (load() dedups by segment id — see forceCompleteLiveSet).
func (m *Manager) completeHNSWLiveSet(ctx context.Context, gt kgtypes.GraphType, name string) (map[searchengine.SegmentID]struct{}, error) {
	live := make(map[searchengine.SegmentID]struct{})

	ids, err := m.managerFor(gt, name).forceCompleteLiveSet(ctx)
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		live[id] = struct{}{}
	}

	return live, nil
}

// completeBM25LiveSet is the COMPLETE live set for a graph's BM25 L2 cache root:
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
		// HNSW pool — embed ∪ deterministic live set, shared HNSW L2 root.
		hnswLive, err := m.completeHNSWLiveSet(ctx, g.GraphType, g.Name)
		if err != nil {
			return PruneCacheReport{}, err
		}
		hnswDir := graphCacheDirFor(m.cacheDir, g.GraphType, g.Name, hnsw.New().Name())
		if err := m.prunePoolReport(&report, g, hnsw.New().Name(), hnswDir, hnswLive, execute); err != nil {
			return PruneCacheReport{}, err
		}

		// BM25 pool — single engine, its own L2 root under the BM25 format name, no union.
		bm25Live, err := m.completeBM25LiveSet(ctx, g.GraphType, g.Name)
		if err != nil {
			return PruneCacheReport{}, err
		}
		bm25Dir := graphCacheDirFor(m.cacheDir, g.GraphType, g.Name, bm25.New().Name())
		if err := m.prunePoolReport(&report, g, bm25.New().Name(), bm25Dir, bm25Live, execute); err != nil {
			return PruneCacheReport{}, err
		}
	}
	return report, nil
}

// prunePoolReport is the per-(graph, format) tail shared by the HNSW and BM25 pools:
// the empty-live-set refusal, the on-disk diff, and (execute only) the
// direct-os.Remove unlink. It appends exactly one PruneCacheGraphReport to report and
// accumulates the EXECUTED totals. Non-generic so both engine instantiations route
// through one body; the caller supplies the already-computed live set for the pool.
//
// THE EMPTY-LIVE-SET REFUSAL IS THE ONE THING STANDING BETWEEN THIS FUNCTION AND A
// CORPUS WIPE, and it is required work in this change rather than a hardening
// afterthought. Every on-disk id absent from `live` is appended to Orphans and, under
// execute, unlinked. So an EMPTY live set orphans and unlinks EVERY .seg file in the
// pool. Nothing else in this body guards it.
//
// It used to be guarded incidentally, by a caller-supplied subset check against the
// source's List(0): an empty live set arrived here only on a path that check covered.
// That check compared the live set against a remote manifest and is gone — locally it
// would compare the L2 cache against itself. Removing it without putting this in its
// place would have made the wipe reachable on the one destructive path this ticket
// leaves universal.
//
// This is the corpus-wipe property — an empty live set must NEVER drive a destructive
// sweep — restated at its second reachable site. The first is the layer swap, where
// prospectiveLayerOK enforces it.
//
// THE REFUSAL IS REPORTED, NOT SILENT. It appends an Aborted pool with a reason
// naming the empty live set, because an unreported refusal is indistinguishable from
// a pool that had nothing to prune — both render zero orphans, and an operator would
// read "nothing to do" where the truth is "declined to act".
//
// A pool whose live set AND on-disk set are both empty is not refused: there is
// nothing to destroy, so it reports as an ordinary empty pool.
//
// The orphan unlink is a DIRECT os.Remove(filepath.Join(dir, id+".seg")) — NEVER the
// index-gated diskSegmentCache.Remove. Remove only reaches os.Remove when the id is
// in the cache's in-memory index, which scanExisting populates ONCE at construction;
// an orphan that appeared after construction (or simply was never resident) is absent
// from that index, so cache.Remove would silently no-op and leave the .seg on disk.
// An orphan is by definition not in the live set and not resident, so a direct unlink
// is the obviously-safe and complete op, and it handles the SHARED HNSW root
// uniformly (one .seg file per id, one os.Remove unlinks it for both engines).
func (m *Manager) prunePoolReport(
	report *PruneCacheReport,
	g PruneCacheTarget,
	format, dir string,
	live map[searchengine.SegmentID]struct{},
	execute bool,
) error {
	onDisk, err := listOnDiskSegIDs(dir)
	if err != nil {
		return err
	}

	if len(live) == 0 && len(onDisk) > 0 {
		// EMPTY LIVE SET OVER A POPULATED POOL. Every .seg on disk would be classified
		// an orphan and unlinked. REFUSE the pool untouched and say so.
		report.Graphs = append(report.Graphs, PruneCacheGraphReport{
			GraphType: g.GraphType,
			Name:      g.Name,
			Format:    format,
			Aborted:   true,
			AbortReason: "empty live set over " + itoa(len(onDisk)) +
				" on-disk segments — refusing to prune, this would remove the whole pool",
		})
		return nil
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

// itoa keeps the refusal reason free of a fmt import in a file that otherwise has
// none.
func itoa(n int) string { return strconv.Itoa(n) }
