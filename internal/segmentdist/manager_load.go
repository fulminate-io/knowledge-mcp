// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// load builds the engine's resident set L2-FIRST: the PRIMARY path is a
// server-independent import of the L2 disk cache, and the cloud (L3) server is a
// FALLBACK reached only when the L2 cache is genuinely cold. This is the spine of
// the L2-primary hierarchy: L2 (local disk ~/.knowledge/segments) is the primary
// source; L3 (cloud __segments) is hit only for a cold cache / background
// reconcile. Startup is independent of the cloud BY DESIGN.
//
//   - EVICTED (taken FIRST, before the guard): the residency budget unloaded this
//     pool to reclaim memory (manager_residency.go). Re-materialize it STRICTLY —
//     reload(evictedIDs, tolerateMisses=false) over the exact set evictResident
//     unloaded and PROVED L2-resident before unloading it. Strict is the whole
//     point: the tolerant mode logs-and-swallows a failed Fetch and imports only
//     the available L2 hits, which for an evict/reload cycle is a SILENT SHORT HIT
//     LIST — an evicted pool must be indistinguishable to a searcher from a
//     never-loaded one, so a re-materialization that cannot complete ERRORS. It
//     reloads evictedIDs rather than cache.Keys() for the same reason: the gate
//     verified that set, and re-deriving from the cache would import whatever
//     happens to be on disk instead.
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
//   - FALLBACK (cold L2, CLOUD path only): loadFromServer Lists the delta (generation >
//     importedGen), serves cache HITS locally, batch-Fetches the MISSES, warms the
//     cache, Imports the full set, and advances importedGen. A cold process has
//     importedGen==0, so List(0) returns the full stored corpus.
//   - OSS L2-AUTHORITATIVE (l2Authoritative): a genuinely-cold L2 has NOTHING to
//     recover FROM — there is no cloud segment registry, and the OSS source's Fetch is
//     L2-only. load() returns with an empty engine; the boot-delay/periodic heal then
//     rebuilds from the local embedded node graph. loadFromServer is UNREACHABLE on
//     this path (decouple #3).
func (m *distManager[Q, S]) load(ctx context.Context) error {
	// EVICTED: strict re-materialization of the exact unloaded set, ahead of the
	// l2Loaded guard (evictResident cleared that guard, so this branch is what the
	// pool's next consumer touch reaches).
	if m.evicted.Load() {
		m.resMu.Lock()
		ids := make([]searchengine.SegmentID, len(m.evictedIDs))
		copy(ids, m.evictedIDs)
		m.resMu.Unlock()
		if err := m.reload(ctx, ids, false); err != nil {
			return err
		}
		m.markMaterialized()
		m.l2Loaded.Store(true)
		return nil
	}
	if m.l2Loaded.Load() {
		return nil
	}
	// PRIMARY: import the L2-resident set with zero network. loadResidentFromL2
	// returns nil after a successful reload(Keys()); on a genuinely-cold cache
	// (no Keys) it returns the sentinel errL2CacheCold, signaling the fallback.
	if err := m.loadResidentFromL2(ctx); err == nil {
		m.l2Loaded.Store(true)
		m.markMaterialized()
		return nil
	} else if err != errL2CacheCold {
		return err
	}
	// OSS L2-AUTHORITATIVE: the cold-L2 server-Fetch fallback is unreachable — there
	// is no server to recover from. Return an empty engine and set the guard; the heal
	// rebuilds from the local embedded nodes (Phase 3 collapse step).
	if m.l2Authoritative {
		m.l2Loaded.Store(true)
		m.markMaterialized()
		return nil
	}
	// FALLBACK (cloud path): cold L2 — pull the corpus from the server, then set the guard.
	if err := m.loadFromServer(ctx); err != nil {
		return err
	}
	m.l2Loaded.Store(true)
	m.markMaterialized()
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

	// Seed the imported segments' live bits from the known tombstones. A blob listed
	// here may predate a delete, and an unseeded import starts every member LIVE —
	// which is exactly how a removed node comes back into the searchable set.
	if err := m.engine.Import(blobs, m.knownTombstones()); err != nil {
		return err
	}
	m.recordResident(blobs)
	m.advanceGen(&m.importedGen, clampedLoadFloor(listedMaxGen, maxGen, minUnfetchedKeptGen))
	return nil
}

// knownTombstones reads the owner's current tombstone set, or nil when this engine
// has no supplier (a directly-constructed test engine). Read per import rather than
// held, so ids learned since the engine was built still apply.
func (m *distManager[Q, S]) knownTombstones() []searchengine.ExternalID {
	if m.tombstoneSeed == nil {
		return nil
	}
	return m.tombstoneSeed()
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
//
// mappedCacheHit is one L2 hit resolved as a mapping: the bytes plus the closure
// that frees them. The release travels with the blob into the engine, which
// hands it to a cleanup keyed on the resulting entry's reachability — reload
// itself must never call it, because the bytes outlive this function.
type mappedCacheHit struct {
	bytes   []byte
	release func()
}

func (m *distManager[Q, S]) reload(ctx context.Context, ids []searchengine.SegmentID, tolerateMisses bool) error {
	if len(ids) == 0 {
		return nil
	}
	blobs := make([]searchengine.SegmentBlob, 0, len(ids))
	var missIDs []searchengine.SegmentID
	cached := make(map[searchengine.SegmentID]mappedCacheHit, len(ids))
	for _, id := range ids {
		data, release, ok, err := m.cache.GetMapped(id)
		if err != nil {
			// FAIL LOUD. The id is cached but unmappable, and the alternative —
			// treating it as a miss and re-reading the bytes onto the heap — is
			// a silently degraded lane that would hide a broken mapping seam on
			// exactly the platform CI never runs, while quietly reinstating the
			// memory profile this seam exists to remove.
			return fmt.Errorf("segmentdist: reload %s: %w", id, err)
		}
		if ok {
			cached[id] = mappedCacheHit{bytes: data, release: release}
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
				// Cache first, then MAP what was cached, so the heap copy the
				// network handed us dies at the end of this call instead of
				// becoming the resident payload.
				m.cache.Put(b.ID, b.Bytes)
				data, release, ok, err := m.cache.GetMapped(b.ID)
				if err != nil {
					return fmt.Errorf("segmentdist: reload fetched %s: %w", b.ID, err)
				}
				if !ok {
					return fmt.Errorf(
						"segmentdist: reload fetched %s: the L2 cache did not retain it, so it cannot be mapped; "+
							"importing the network copy instead would make the segment heap-resident", b.ID)
				}
				cached[b.ID] = mappedCacheHit{bytes: data, release: release}
			}
		}
	}
	for _, id := range ids {
		hit, ok := cached[id]
		if !ok {
			continue
		}
		blobs = append(blobs, searchengine.SegmentBlob{ID: id, Bytes: hit.bytes, Release: hit.release})
	}
	// Same seed as loadFromServer: a reload re-imports stored blobs, and one of them
	// may predate a delete.
	if err := m.engine.Import(blobs, m.knownTombstones()); err != nil {
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
		seg := residentSeg{mappedBytes: len(b.Bytes), format: b.Format, generation: b.Generation}
		if seg.format == "" {
			seg.format = prev.format
		}
		if seg.generation == 0 {
			seg.generation = prev.generation
		}
		m.resident[b.ID] = seg
	}
}
