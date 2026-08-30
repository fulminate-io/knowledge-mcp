// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// load builds the engine's resident set from the L2 disk cache. EVERY branch is
// ZERO-NETWORK: the L2 cache (local disk ~/.knowledge/segments) is the only
// segment source there is, so startup is independent of the cloud not by design
// preference but by construction — there is nothing else to consult.
//
//   - EVICTED (taken FIRST, before the guard): the residency budget unloaded this
//     pool to reclaim memory (manager_residency.go). Re-materialize it STRICTLY —
//     reload(evictedIDs, tolerateMisses=false) over the exact set evictResident
//     unloaded and PROVED L2-resident before unloading it. Strict is the whole
//     point: the tolerant mode logs an L2 miss and imports only the available hits,
//     which for an evict/reload cycle is a SILENT SHORT HIT LIST — an evicted pool
//     must be indistinguishable to a searcher from a never-loaded one, so a
//     re-materialization that cannot complete ERRORS. It reloads evictedIDs rather
//     than cache.Keys() for the same reason: the gate verified that set, and
//     re-deriving from the cache would import whatever happens to be on disk
//     instead.
//   - GUARD: l2Loaded short-circuits a repeated load() to a bare return nil — the
//     resident set is already imported this process (the "Load is idempotent"
//     contract manager_search.go relies on).
//   - PRIMARY (populated L2): cache.Keys() enumerates the L2-resident ids and
//     reload() imports them — a cache HIT for every id on a populated-cache
//     restart. See loadResidentFromL2 for what that set IS (a superset across an
//     un-reclaimed merge window, narrowed at Import by the stored blobs' own
//     supersession records — never degenerate/empty).
//   - COLD: a genuinely-cold L2 has NOTHING to recover FROM. load() returns with an
//     empty engine and sets the guard; the boot-delay/periodic heal then rebuilds
//     from the local embedded node graph.
//
// CTX IS CURRENTLY UNREAD, AND THAT IS A KNOWN CONSEQUENCE OF DELETING THE CLOUD
// RAIL rather than an oversight here. Every leg of this path is now local disk and
// memory — there is no network call left to cancel — so the context reaches the
// bottom of the subtree and is consulted by nothing. Removing it does not stop at
// this method: load's callers, forceCompleteLiveSet, and the *Manager methods above
// them would each lose their ctx in turn, which is a change to segmentdist's public
// surface and not one a lint sweep gets to make. The open question is whether this
// subtree should honor cancellation again — a ctx.Err() check before each segment
// import, which a large cold-cache import would genuinely benefit from — or shed ctx
// entirely and be honest about being a local-only path. Either answer deletes this
// exemption; leaving both undone is what makes the parameter misleading.
//
//nolint:unparam // ctx is unread since the cloud rail's deletion; see the note above — removing it cascades into segmentdist's public Manager API
func (m *distManager[Q, S]) load(ctx context.Context) error {
	// EVICTED: strict re-materialization of the exact unloaded set, ahead of the
	// l2Loaded guard (evictResident cleared that guard, so this branch is what the
	// pool's next consumer touch reaches).
	if m.evicted.Load() {
		m.resMu.Lock()
		ids := make([]searchengine.SegmentID, len(m.evictedIDs))
		copy(ids, m.evictedIDs)
		m.resMu.Unlock()
		if err := m.reload(ids, false); err != nil {
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
	if err := m.loadResidentFromL2(); err == nil {
		m.l2Loaded.Store(true)
		m.markMaterialized()
		return nil
	} else if err != errL2CacheCold {
		return err
	}
	// COLD: a genuinely-cold L2 has NOTHING to recover FROM — the L2 cache is the
	// only segment source. Return an empty engine and set the guard; the
	// boot-delay/periodic heal then rebuilds from the local embedded node graph.
	m.l2Loaded.Store(true)
	m.markMaterialized()
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

// errL2CacheCold is the sentinel loadResidentFromL2 returns when the L2 disk cache
// is genuinely cold/wiped (cache.Keys() is empty) — there is no resident set to
// import. load() treats it as the signal to return an empty engine and leave the
// heal to rebuild; it is NOT a real error and must never surface to a caller as a
// load failure.
var errL2CacheCold = errors.New("segmentdist: L2 cache is cold (no resident ids)")

// loadResidentFromL2 is the PRIMARY import path of load(): it reconstructs the
// resident set from the L2 disk cache, which is the only place a segment lives.
//
// It enumerates this manager's L2-resident ids and imports them via
// reload(tolerateMisses=true), which is a cache HIT for every id on a
// populated-cache restart. A content-hash filename names the segment PAYLOAD (see
// diskSegmentCache), so the import is safe. tolerateMisses=true because the one
// miss this path can see is an id removed between the Keys() snapshot and the
// read — a Remove racing the snapshot — and the available hits are worth more than
// an abort.
//
// WHAT THE RESIDENT SET IS: whatever is on disk, MINUS what the stored blobs
// themselves record as superseded. The merge-reclaim ordering (reclaimMerged Puts the
// merged blob BEFORE Removing its constituents) permits a window where both a merged
// blob and its superseded constituents are on disk at once — so this enumerates a
// superset, and Import declines the constituents on the consolidated blob's own
// supersession record (searchengine/supersession.go) rather than publishing both. Before
// that record existed the superset was published whole, which is how a load could put a
// deleted document back into the searchable set. The set is never degenerate/empty and
// each blob is named by the hash of its own payload. This was once a trade-off
// against consulting a server manifest; with the L2 cache authoritative the stored blobs
// ARE the manifest.
//
// On an empty L2 cache it returns errL2CacheCold (the cold-cache signal); load()
// then returns an empty engine and leaves the rebuild to the heal.
func (m *distManager[Q, S]) loadResidentFromL2() error {
	keys := m.cache.Keys()
	if len(keys) == 0 {
		return errL2CacheCold
	}
	slog.Info("segmentdist: importing L2-resident segments server-independently (L2-first)",
		"target", m.target.GetName(), "format", m.format, "l2_ids", len(keys))
	if err := m.reload(keys, true); err != nil {
		return err
	}
	slog.Info("segmentdist: imported L2-resident segments without the server",
		"target", m.target.GetName(), "format", m.format, "imported", len(keys))
	return nil
}

// reload re-materializes previously-unloaded segments from the L2 cache and
// engine.Imports them; re-adds them to resident tracking. There is NO network leg:
// the L2 cache is the only segment source, so an id that misses the cache here has
// missed the one place it could have been recovered from.
//
// tolerateMisses governs what an L2 MISS means:
//   - false (re-materialize callers, e.g. unload-then-reload): the whole reload
//     aborts and errors, naming the absent ids. The evicted-pool caller needs this —
//     "an evicted pool must be indistinguishable to a searcher from a never-loaded
//     one, so a re-materialization that cannot complete ERRORS".
//   - true (the L2-first path, loadResidentFromL2): the miss is logged and reload
//     imports the AVAILABLE L2 hits — serving the partial, content-hash-
//     content-addressed superset rather than aborting because one constituent was
//     removed between the Keys() snapshot and this read (a Remove racing the
//     snapshot, see loadResidentFromL2's TRADE-OFF).
//
// mappedCacheHit is one L2 hit resolved as a mapping: the bytes plus the closure
// that frees them. The release travels with the blob into the engine, which
// hands it to a cleanup keyed on the resulting entry's reachability — reload
// itself must never call it, because the bytes outlive this function.
type mappedCacheHit struct {
	bytes   []byte
	release func()
}

func (m *distManager[Q, S]) reload(ids []searchengine.SegmentID, tolerateMisses bool) error {
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
		if !tolerateMisses {
			// UNRECOVERABLE, not "the server is down". The L2 cache is the only
			// segment source, so an id that missed GetMapped above has already missed
			// the one place it could have come from; there is nothing left to try.
			return fmt.Errorf(
				"segmentdist: reload %s/%s: %d of %d ids are absent from the L2 cache and there is no source to recover them from: %v",
				m.target.GetName(), m.format, len(missIDs), len(ids), missIDs)
		}
		// L2-first: import the available (content-addressed) cache hits instead of
		// aborting. A miss here is an id removed between the Keys() snapshot and
		// this read.
		slog.Warn("segmentdist: L2-first reload found ids absent from the L2 cache; "+
			"importing the available L2 hits only",
			"target", m.target.GetName(), "format", m.format,
			"missing", len(missIDs), "available", len(cached))
	}
	for _, id := range ids {
		hit, ok := cached[id]
		if !ok {
			continue
		}
		// SPLIT AT THE LOAD BOUNDARY, into two zero-copy subslices of the one
		// mapping, so every blob in this process satisfies the same invariant
		// regardless of whether it came from an engine producer or off disk.
		envelope, payload, splitErr := searchengine.SplitStoredBlob(hit.bytes)
		if splitErr != nil {
			if hit.release != nil {
				hit.release()
			}
			return fmt.Errorf("segmentdist: stored segment %s carries a damaged supersession envelope: %w", id, splitErr)
		}
		blobs = append(blobs, searchengine.SegmentBlob{
			ID: id, Bytes: payload, Envelope: envelope, Release: hit.release,
		})
	}
	// A reload re-imports stored blobs, and one of them
	// may predate a delete.
	if err := m.engine.Import(blobs, m.knownTombstones()); err != nil {
		return err
	}
	// ONLY WHAT THE ENGINE ACTUALLY PUBLISHED IS RECORDED RESIDENT. Import DECLINES a
	// blob another blob in the same batch records as superseded, so the set handed to
	// it is a superset of the set it publishes. Recording the superset would meter
	// heap for segments the engine does not hold and would name them in evictedIDs,
	// where a strict re-materialization would then demand blobs the engine never
	// wanted. The engine's own resident set is the authority, and asking it is what
	// keeps that authority single.
	m.recordResident(publishedOf(blobs, m.engine.ResidentSegmentIDs()))
	return nil
}

// publishedOf narrows a batch to the blobs the engine is now serving.
func publishedOf(blobs []searchengine.SegmentBlob, resident []searchengine.SegmentID) []searchengine.SegmentBlob {
	live := make(map[searchengine.SegmentID]struct{}, len(resident))
	for _, id := range resident {
		live[id] = struct{}{}
	}
	kept := make([]searchengine.SegmentBlob, 0, len(blobs))
	for _, b := range blobs {
		if _, ok := live[b.ID]; ok {
			kept = append(kept, b)
		}
	}
	return kept
}

// recordResident records imported blobs in the resident-bytes tracking. The
// format/generation are preserved when known (load supplies them; reload re-uses
// the prior values if present).
func (m *distManager[Q, S]) recordResident(blobs []searchengine.SegmentBlob) {
	m.resMu.Lock()
	defer m.resMu.Unlock()
	for _, b := range blobs {
		prev := m.resident[b.ID]
		// THE WHOLE STORED FILE, envelope included. mappedBytes is documented as the
		// ENCODED blob length and is summed as the on-disk total the residency
		// pressure signal reports beside heap, so counting the payload alone would
		// under-report every enveloped segment — silently, because nothing else reads
		// this number.
		seg := residentSeg{mappedBytes: len(b.Envelope) + len(b.Bytes), format: b.Format, generation: b.Generation}
		if seg.format == "" {
			seg.format = prev.format
		}
		if seg.generation == 0 {
			seg.generation = prev.generation
		}
		m.resident[b.ID] = seg
	}
}
