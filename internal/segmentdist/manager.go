// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// maxFetchSegmentIDs caps how many segment ids a single Fetch RPC may request.
// On a cold/force-load the client lists the whole accumulated-generation delta
// and must materialize every miss; issuing ONE Fetch(allMisses) made the server
// build the entire corpus into one slice (~1.9 GiB) and OOM (the 2026-06-19 P0).
// Sub-batching the misses into chunks of at most this many ids bounds the client's
// peak resident bytes to ~one chunk's worth of blobs.
//
// This is a COUNT cap, not a byte cap: SegmentMetaProto carries no byte size
// (adding one is explicitly OUT OF SCOPE — CEO Option A), so the client cannot
// byte-pack. It is sized so maxFetchSegmentIDs × a generous per-blob size stays
// well under the server's authoritative store.MaxSegmentFetchResponseBytes
// (256 MiB) ceiling: 256 ids × ~256 KiB/blob ≈ 64 MiB, comfortably under. The two
// bounds are deliberately coupled — the count cap is the common case, and the
// server byte ceiling is the hard backstop that triggers the adaptive halving in
// fetchMisses when a count-capped chunk is nonetheless too large in bytes.
// Keeping the cap a parameter of fetchMisses leaves a future count→byte upgrade
// additive without a proto change.
const maxFetchSegmentIDs = 256

// segmentL2Cache is the L2 disk-cache seam distManager writes through. The
// concrete *diskSegmentCache satisfies it; tests substitute instrumented or
// fault-injecting implementations to exercise the prune-safety ordering.
// searchengine.SegmentCache is NOT reused here — it carries only Get/Put, and the
// reclaim/prune paths require Remove, so extending the searchengine contract for a
// segmentdist need would be the wrong boundary.
type segmentL2Cache interface {
	Get(id searchengine.SegmentID) ([]byte, bool)
	Put(id searchengine.SegmentID, b []byte)
	Remove(id searchengine.SegmentID)
	// Keys enumerates the L2-resident segment ids server-independently so load()
	// can reconstruct the resident set from L2 alone when the server manifest is
	// unavailable (slow/down server). It reads only the in-memory index — no disk
	// re-read, no network.
	Keys() []searchengine.SegmentID
}

// distManager ties one graph's searchengine.SegmentedIndex to the SegmentService
// wire: it SHIPS newly-built segments (diffing against what the server already
// holds), LAZILY LOADS the server's delta into the engine (cache-first), and
// UNLOADS / RELOADS resident segments to bound memory. It is generic over the
// engine's [Q, S] format parameters so it works against ANY SegmentFormat (the
// mock format in tests; the real HNSW/BM25 formats once the migration wires the
// engine into client search).
type distManager[Q, S any] struct {
	engine *searchengine.SegmentedIndex[Q, S]
	source *rpcSegmentSource
	cache  segmentL2Cache
	target *knowledgev1.GraphSelector

	// format is this engine's segment format name (e.g. "hnsw", "bm25"). The
	// server keys blobs by graphKey ONLY (segment_store.go: <type>:<name>, no
	// format dimension), so a single graph's List/Fetch returns BOTH this graph's
	// HNSW and BM25 blobs. load/reload MUST drop the blobs of the OTHER format —
	// importing a BM25 blob into the HNSW engine (or vice versa) makes Decode fail
	// ("unsupported binary hnsw serial version"). An empty format means "no
	// filter" (the mock-format engine in tests, which ships its own format only).
	format string

	// importedGen and shippedGen are the DECOUPLED generation cursors. They were
	// once ONE shared cursor, which had an undocumented second job: after a
	// ship() advanced it, a later load()'s List(sharedCursor) excluded this
	// process's own just-shipped tail (strictly-greater filter) so it was not
	// re-imported. But sharing the cursor ALSO let ship() poison the load floor:
	// on a cold process the embed-writeback ship stamps the fresh tail at the
	// server's monotonic generation (~N, next after the existing corpus) and
	// advanced the shared cursor to N BEFORE any search ran — so the first lazy
	// load()'s List(N) returned an empty delta and the N stored blobs were never
	// imported. Search then served a ~2-doc tail until a manual rebuild.
	//
	//   - importedGen is the LOAD floor: the max generation load() has actually
	//     imported into the searchable engine. load() Lists(importedGen) and
	//     advances ONLY importedGen. A cold process has importedGen==0, so the
	//     first load() Lists(0) and imports the FULL stored corpus. (Re-listing
	//     this process's own shipped tail is now harmless: Import is idempotent by
	//     segment ID — see searchengine publishImport — so a re-listed resident
	//     segment is dropped, never double-added.)
	//   - shippedGen is TRACKING-ONLY: the max generation shipNew has stamped this
	//     process. It is advanced by shipNew and never read as a load floor, so a
	//     ship can no longer poison load().
	importedGen atomic.Uint64
	shippedGen  atomic.Uint64

	// shippedIDs is the set of content-hash segment ids already present on the
	// server. SEEDED from Source.List(0) the first time a seed SUCCEEDS (the
	// shipMu-guarded `seeded` latch below) — the server is the single source of
	// truth for what has been shipped; the client RE-DERIVES rather than persisting
	// a drift-prone local file. Guarded by shipMu. Serves TWO purposes: ship-new
	// DIFF suppression (skip re-uploading the seeded corpus), and the ROLE-A
	// authoritative replace-prune used by the deterministic rebuild
	// (FlushDeterministic), whose Export() IS the complete new corpus.
	//
	// locallyShipped is the set of ids THIS PROCESS shipped via shipNew — seeded
	// EMPTY and never populated from the server. It is the ROLE-B prune-eligible
	// set: the embed/tail ship path (AddAndShip/AddAndShipFields/Flush) reconciles
	// merges against locallyShipped so a fresh process (locallyShipped empty after
	// restart) can NEVER prune the prior server corpus it did not itself ship —
	// only this-process merged-away ids. This per-role split is the fix for the
	// segment-ship restart false-prune: seeding shippedIDs from the full server
	// List(0) while Export() returns only the tail made the embed reconcile prune
	// the whole corpus on the first ship after restart.
	shipMu sync.Mutex
	// seeded latches true ONLY when a seed List(0) SUCCEEDS (ensureShippedSeeded,
	// manager_seed.go). A transient List failure leaves it false so the next ship
	// RE-ARMS the seed — replacing the old sync.Once+seedErr, which consumed the
	// Once on the first (possibly failed) attempt and poisoned shipping for the
	// process lifetime. Guarded by shipMu.
	seeded         bool
	shippedIDs     map[searchengine.SegmentID]struct{}
	locallyShipped map[searchengine.SegmentID]struct{}

	// resident tracks the segments currently imported into the engine + an
	// approximate resident-byte total (sum of imported blob byte lengths). Guarded
	// by resMu. unloaded holds the bytes of segments dropped under pressure so
	// reload can re-Import from L2 without a network round-trip.
	resMu    sync.Mutex
	resident map[searchengine.SegmentID]residentSeg

	// recovering single-flights the read-side degeneracy backstop (recoverIfDegenerate
	// in manager_backstop.go): the FIRST search to find a degenerate engine CASes it
	// true, resets the load floor, and re-imports the corpus; concurrent searches see
	// it already set and skip (the recovery will make the corpus resident shortly).
	recovering atomic.Bool

	// l2Loaded is the L2-first once-guard. load() is L2-PRIMARY: the FIRST act is a
	// server-independent import of the L2-resident set (cache.Keys() -> reload()),
	// not a server List. Once that primary import (or the cold-cache List+Fetch
	// fallthrough) has run, l2Loaded is set true and a repeated load() short-circuits
	// to a bare return nil — matching the "Load is idempotent" contract that
	// manager_search.go relies on. Modeled on recovering: a one-shot atomic.Bool, no
	// lock.
	l2Loaded atomic.Bool
}

// residentSeg records one imported segment's size + format + generation so unload
// accounting and reload re-import are exact.
type residentSeg struct {
	bytes      int
	format     string
	generation uint64
}

// newDistManager wires a manager for one graph. format is the engine's segment
// format name used to filter the server's per-graph (format-agnostic) blob list
// down to THIS engine's format on load/reload; pass "" to disable filtering (the
// test mock format, which is the only format its graph ever ships).
func newDistManager[Q, S any](
	engine *searchengine.SegmentedIndex[Q, S],
	source *rpcSegmentSource,
	cache segmentL2Cache,
	target *knowledgev1.GraphSelector,
	format string,
) *distManager[Q, S] {
	return &distManager[Q, S]{
		engine:         engine,
		source:         source,
		cache:          cache,
		target:         target,
		format:         format,
		shippedIDs:     make(map[searchengine.SegmentID]struct{}),
		locallyShipped: make(map[searchengine.SegmentID]struct{}),
		resident:       make(map[searchengine.SegmentID]residentSeg),
	}
}

// keepFormat reports whether a blob/meta tagged f belongs to this engine's
// format. An empty distManager.format disables the filter (test mock format).
func (m *distManager[Q, S]) keepFormat(f string) bool {
	return m.format == "" || f == m.format
}

// load builds the engine's resident set L2-FIRST: the PRIMARY path is a
// server-independent import of the L2 disk cache, and the cloud (L3) server is a
// FALLBACK reached only when the L2 cache is genuinely cold. This is the spine of
// the L2-primary hierarchy: L2 (local disk ~/.knowledge/segments) is the primary
// source; L3 (cloud __segments) is hit only for a cold cache / background
// reconcile. Startup is independent of the cloud BY DESIGN.
//
//   - GUARD: l2Loaded short-circuits a repeated load() to a bare return nil — the
//     resident set is already imported this process (the "Load is idempotent"
//     contract manager_search.go relies on).
//   - PRIMARY (populated L2): cache.Keys() enumerates the L2-resident ids
//     server-independently and reload() imports them — a cache HIT for every id on
//     a populated-cache restart, ZERO network. This path does NOT advance
//     importedGen: no server manifest was obtained, so the load floor stays put and
//     the later background reconcile re-Lists from the same floor for the genuine
//     server delta. See loadResidentFromL2 for the trade-off (un-manifest-filtered
//     superset across an un-reclaimed merge window — never degenerate/empty).
//   - FALLBACK (cold L2): loadFromServer Lists the delta (generation >
//     importedGen), serves cache HITS locally, batch-Fetches the MISSES, warms the
//     cache, Imports the full set, and advances importedGen. A cold process has
//     importedGen==0, so List(0) returns the full stored corpus.
func (m *distManager[Q, S]) load(ctx context.Context) error {
	if m.l2Loaded.Load() {
		return nil
	}
	// PRIMARY: import the L2-resident set with zero network. loadResidentFromL2
	// returns nil after a successful reload(Keys()); on a genuinely-cold cache
	// (no Keys) it returns the sentinel errL2CacheCold, signaling the L3 fallback.
	if err := m.loadResidentFromL2(ctx); err == nil {
		m.l2Loaded.Store(true)
		return nil
	} else if err != errL2CacheCold {
		return err
	}
	// FALLBACK: cold L2 — pull the corpus from the server, then set the guard.
	if err := m.loadFromServer(ctx); err != nil {
		return err
	}
	m.l2Loaded.Store(true)
	return nil
}

// loadFromServer is the COLD-L2 fallback path: List the server delta (generation >
// importedGen), serve cache HITS locally (skip network), batch-Fetch the MISSES,
// warm the cache, and Import the full set. Advances importedGen (the LOAD floor —
// NOT shippedGen) to the max generation in the delta. A cold process has
// importedGen==0, so List(0) returns the full stored corpus and imports it all;
// re-listing this process's own shipped tail is harmless because Import is
// idempotent by segment id. tombstones is empty — tombstone sourcing is the
// overlay/migration ticket's concern, not this layer.
func (m *distManager[Q, S]) loadFromServer(ctx context.Context) error {
	listed, err := m.source.List(ctx, m.importedGen.Load())
	if err != nil {
		// This is the COLD-L2 fallback: load() only reaches here after the L2-first
		// primary path found an empty cache (loadResidentFromL2 returned
		// errL2CacheCold). So there is nothing to fall back to — a populated L2 would
		// already have been imported on the primary path, server-independently. Return
		// the original List error so the caller surfaces the genuine cold-cache +
		// down-server condition.
		return err
	}
	// The server bucket holds BOTH formats for this graph (no format dimension in
	// the graphKey); keep only this engine's format so we never decode a foreign
	// blob. Track the max generation across the FULL listed delta (incl. dropped
	// foreign-format metas) so importedGen still advances past them and a later
	// load does not re-list them.
	metas := make([]searchengine.SegmentMeta, 0, len(listed))
	var listedMaxGen uint64
	for _, meta := range listed {
		if meta.Generation > listedMaxGen {
			listedMaxGen = meta.Generation
		}
		if m.keepFormat(meta.Format) {
			metas = append(metas, meta)
		}
	}
	if len(metas) == 0 {
		m.advanceGen(&m.importedGen, listedMaxGen)
		return nil
	}

	// metaByID + cache-hit collection.
	type pending struct {
		meta  searchengine.SegmentMeta
		bytes []byte
		hit   bool
	}
	pend := make([]pending, len(metas))
	var missIDs []searchengine.SegmentID
	for i, meta := range metas {
		if b, ok := m.cache.Get(meta.ID); ok {
			pend[i] = pending{meta: meta, bytes: b, hit: true}
			continue
		}
		pend[i] = pending{meta: meta}
		missIDs = append(missIDs, meta.ID)
	}

	// Sub-batched Fetch for all misses: fetchMisses count-caps each Fetch RPC
	// (and halves on the server byte ceiling) so a cold load never issues one
	// unbounded Fetch(allMisses) — the 2026-06-19 OOM. A byte ceiling that a single
	// blob cannot satisfy hard-errors here BEFORE the Import + importedGen advance
	// below, so the unfetched id stays re-listable. fetchMisses may also return a
	// SHORT-but-OK subset (the server omitted some listed ids); the load-floor
	// clamp below keeps those omitted kept-format segments re-listable too.
	if len(missIDs) > 0 {
		blobs, err := m.fetchMisses(ctx, missIDs)
		if err != nil {
			return err
		}
		fetched := make(map[searchengine.SegmentID][]byte, len(blobs))
		for _, b := range blobs {
			fetched[b.ID] = b.Bytes
			m.cache.Put(b.ID, b.Bytes)
		}
		for i := range pend {
			if !pend[i].hit {
				pend[i].bytes = fetched[pend[i].meta.ID]
			}
		}
	}

	// Every meta in pend is KEPT-format (metas was already keepFormat-filtered). A
	// nil-bytes entry is a kept-format segment the server LISTED but did not Fetch
	// (a short-but-OK Fetch — e.g. a refcount-GC raced between List and Fetch). Track
	// the LOWEST such generation: the load floor must NOT advance to or past it, or
	// the omitted segment becomes permanently unre-listable and is silently lost.
	blobs := make([]searchengine.SegmentBlob, 0, len(pend))
	var maxGen, minUnfetchedKeptGen uint64
	for _, p := range pend {
		if p.bytes == nil {
			// Kept-format meta the server could not Fetch — skip the import but
			// remember its generation as a clamp on the load-floor advance.
			if minUnfetchedKeptGen == 0 || p.meta.Generation < minUnfetchedKeptGen {
				minUnfetchedKeptGen = p.meta.Generation
			}
			continue
		}
		blobs = append(blobs, searchengine.SegmentBlob{
			ID:         p.meta.ID,
			Format:     p.meta.Format,
			Generation: p.meta.Generation,
			Bytes:      p.bytes,
		})
		if p.meta.Generation > maxGen {
			maxGen = p.meta.Generation
		}
	}

	if err := m.engine.Import(blobs, nil); err != nil {
		return err
	}
	m.recordResident(blobs)
	m.advanceGen(&m.importedGen, clampedLoadFloor(listedMaxGen, maxGen, minUnfetchedKeptGen))
	return nil
}

// clampedLoadFloor computes the importedGen advance target for a loadFromServer
// pass. The base is the full listed delta's max generation (listedMaxGen, incl.
// dropped foreign-format metas — re-listing those is wasteful, not lossy, so they
// never clamp) raised to the max IMPORTED generation. When a kept-format segment's
// blob was omitted by a short Fetch (minUnfetchedKeptGen > 0), the floor is clamped
// to minUnfetchedKeptGen-1 so that segment stays re-listable on the next load and
// is never silently lost.
func clampedLoadFloor(listedMaxGen, maxImportedGen, minUnfetchedKeptGen uint64) uint64 {
	target := max(listedMaxGen, maxImportedGen)
	if minUnfetchedKeptGen > 0 && minUnfetchedKeptGen-1 < target {
		target = minUnfetchedKeptGen - 1
	}
	return target
}

// errL2CacheCold is the sentinel loadResidentFromL2 returns when the L2 disk cache
// is genuinely cold/wiped (cache.Keys() is empty) — there is no resident set to
// import server-independently. load() treats it as the signal to fall through to
// the cold-L2 server fallback (loadFromServer); it is NOT a real error and must
// never surface to a caller as a load failure.
var errL2CacheCold = errors.New("segmentdist: L2 cache is cold (no resident ids)")

// loadResidentFromL2 is the SERVER-INDEPENDENT PRIMARY import path of load(): it
// reconstructs the resident set from the L2 disk cache alone, so startup never
// depends on the cloud (L3) and a slow/down server never leaves the engine empty
// nor lets a caller mistake the failure for genuine degeneracy and rebuild from
// scratch.
//
// It enumerates this manager's L2-resident ids and imports them via
// reload(tolerateMisses=true), which is a cache HIT for every id on a
// populated-cache restart — ZERO network. The Fetch inside reload() is reserved
// for ids genuinely missing from L2 (none on a cold restart over a populated
// cache); content-hash filenames are self-verifying, so an L2-only import is safe.
// tolerateMisses=true (SERVER-INDEPENDENT path): a miss that cannot be Fetched
// because the server is down still imports the available L2 hits rather than
// aborting — see the merge-reclaim race in the TRADE-OFF below.
//
// TRADE-OFF (accepted): this L2-resident set is UN-MANIFEST-FILTERED — it is
// whatever is on disk, not the server's authoritative current manifest. The
// merge-reclaim ordering (reclaimMerged Puts the merged blob BEFORE Removing its
// constituents) permits a window where both a merged blob and its superseded
// constituents are on disk at once, so the imported set can be a stale-but-valid
// SUPERSET across an un-reclaimed merge window. It is never degenerate/empty, each
// blob self-verifies by its content-hash filename, and the superset self-corrects
// on the next live merge. This is the accepted trade versus a from-scratch rebuild
// when the server is unreachable.
//
// It does NOT advance importedGen: the server manifest was never obtained, so the
// load floor must stay put — the later background reconcile re-Lists from the same
// floor and reconciles the genuine server delta.
//
// On an empty L2 cache it returns errL2CacheCold (the cold-cache signal); load()
// falls through to loadFromServer.
func (m *distManager[Q, S]) loadResidentFromL2(ctx context.Context) error {
	keys := m.cache.Keys()
	if len(keys) == 0 {
		return errL2CacheCold
	}
	slog.Info("segmentdist: importing L2-resident segments server-independently (L2-first)",
		"target", m.target.GetName(), "format", m.format, "l2_ids", len(keys))
	if err := m.reload(ctx, keys, true); err != nil {
		return err
	}
	slog.Info("segmentdist: imported L2-resident segments without the server",
		"target", m.target.GetName(), "format", m.format, "imported", len(keys))
	return nil
}

// reload re-materializes previously-unloaded segments: L2 cache HIT (no network)
// or Source.Fetch on a miss, then engine.Import; re-adds them to resident tracking.
// tolerateMisses governs a failed Source.Fetch for the L2 MISSES (server down):
//   - false (re-materialize callers, e.g. unload-then-reload): the unloaded
//     segment genuinely needs the server, so the whole reload aborts and errors.
//   - true (the L2-first server-independence path, loadResidentFromL2): the Fetch
//     failure is logged-and-swallowed and reload imports the AVAILABLE L2 hits —
//     serving the partial, content-hash-self-verifying superset rather than
//     aborting because one constituent was evicted (a Remove racing the Keys()
//     snapshot, see loadResidentFromL2's TRADE-OFF) AND the server is down.
func (m *distManager[Q, S]) reload(ctx context.Context, ids []searchengine.SegmentID, tolerateMisses bool) error {
	if len(ids) == 0 {
		return nil
	}
	blobs := make([]searchengine.SegmentBlob, 0, len(ids))
	var missIDs []searchengine.SegmentID
	cached := make(map[searchengine.SegmentID][]byte, len(ids))
	for _, id := range ids {
		if b, ok := m.cache.Get(id); ok {
			cached[id] = b
			continue
		}
		missIDs = append(missIDs, id)
	}
	if len(missIDs) > 0 {
		// Sub-batched Fetch (count-capped + byte-ceiling halving) so a large
		// reload never issues one unbounded Fetch(allMisses).
		fetched, err := m.fetchMisses(ctx, missIDs)
		switch {
		case err != nil && !tolerateMisses:
			return err
		case err != nil:
			// L2-first: server unreachable for the misses — log and continue with
			// the available (self-verifying) cache hits instead of aborting.
			slog.Warn("segmentdist: L2-first reload could not fetch missed ids from the server; "+
				"importing the available L2 hits only",
				"target", m.target.GetName(), "format", m.format,
				"missed", len(missIDs), "available", len(cached), "err", err)
		default:
			for _, b := range fetched {
				cached[b.ID] = b.Bytes
				m.cache.Put(b.ID, b.Bytes)
			}
		}
	}
	for _, id := range ids {
		b, ok := cached[id]
		if !ok {
			continue
		}
		blobs = append(blobs, searchengine.SegmentBlob{ID: id, Bytes: b})
	}
	if err := m.engine.Import(blobs, nil); err != nil {
		return err
	}
	m.recordResident(blobs)
	return nil
}

// recordResident records imported blobs in the resident-bytes tracking. The
// format/generation are preserved when known (load supplies them; reload re-uses
// the prior values if present).
func (m *distManager[Q, S]) recordResident(blobs []searchengine.SegmentBlob) {
	m.resMu.Lock()
	defer m.resMu.Unlock()
	for _, b := range blobs {
		prev := m.resident[b.ID]
		seg := residentSeg{bytes: len(b.Bytes), format: b.Format, generation: b.Generation}
		if seg.format == "" {
			seg.format = prev.format
		}
		if seg.generation == 0 {
			seg.generation = prev.generation
		}
		m.resident[b.ID] = seg
	}
}

// advanceGen monotonically raises the given cursor to gen (never lowers it). It
// is the ONE CAS loop both decoupled cursors share: load() passes &importedGen
// (the load floor), shipNew passes &shippedGen (ship tracking only).
func (m *distManager[Q, S]) advanceGen(cur *atomic.Uint64, gen uint64) {
	for {
		seen := cur.Load()
		if gen <= seen || cur.CompareAndSwap(seen, gen) {
			return
		}
	}
}
