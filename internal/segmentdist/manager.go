// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"sort"
	"sync"
	"sync/atomic"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

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
	cache  *diskSegmentCache
	target *knowledgev1.GraphSelector

	// format is this engine's segment format name (e.g. "hnsw", "bm25"). The
	// server keys blobs by graphKey ONLY (segment_store.go: <type>:<name>, no
	// format dimension), so a single graph's List/Fetch returns BOTH this graph's
	// HNSW and BM25 blobs. load/reload MUST drop the blobs of the OTHER format —
	// importing a BM25 blob into the HNSW engine (or vice versa) makes Decode fail
	// ("unsupported binary hnsw serial version"). An empty format means "no
	// filter" (the mock-format engine in tests, which ships its own format only).
	format string

	lastSeenGen atomic.Uint64

	// shippedIDs is the set of content-hash segment ids already present on the
	// server. SEEDED ONCE (seedOnce) from Source.List(0) — the server is the
	// single source of truth for what has been shipped; the client RE-DERIVES
	// rather than persisting a drift-prone local file. Guarded by shipMu.
	shipMu     sync.Mutex
	seedOnce   sync.Once
	seedErr    error
	shippedIDs map[searchengine.SegmentID]struct{}

	// resident tracks the segments currently imported into the engine + an
	// approximate resident-byte total (sum of imported blob byte lengths). Guarded
	// by resMu. unloaded holds the bytes of segments dropped under pressure so
	// reload can re-Import from L2 without a network round-trip.
	resMu    sync.Mutex
	resident map[searchengine.SegmentID]residentSeg
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
	cache *diskSegmentCache,
	target *knowledgev1.GraphSelector,
	format string,
) *distManager[Q, S] {
	return &distManager[Q, S]{
		engine:     engine,
		source:     source,
		cache:      cache,
		target:     target,
		format:     format,
		shippedIDs: make(map[searchengine.SegmentID]struct{}),
		resident:   make(map[searchengine.SegmentID]residentSeg),
	}
}

// keepFormat reports whether a blob/meta tagged f belongs to this engine's
// format. An empty distManager.format disables the filter (test mock format).
func (m *distManager[Q, S]) keepFormat(f string) bool {
	return m.format == "" || f == m.format
}

// ensureShippedSeeded lazily seeds shippedIDs from the server's current segment
// set (Source.List(0)) so a fresh process does not re-ship the entire corpus on
// the first ship(). The server is the source of truth; the client re-derives.
// Backed by the idempotent server Put, this seed is an optimization (avoid the
// upload), not a correctness requirement.
func (m *distManager[Q, S]) ensureShippedSeeded(ctx context.Context) error {
	m.seedOnce.Do(func() {
		metas, err := m.source.List(ctx, 0)
		if err != nil {
			m.seedErr = err
			return
		}
		m.shipMu.Lock()
		for _, meta := range metas {
			m.shippedIDs[meta.ID] = struct{}{}
		}
		m.shipMu.Unlock()
	})
	return m.seedErr
}

// ship exports every current segment, diffs against shippedIDs, and ships ONLY
// the new content-hash blobs in one batched Ship. An empty diff is a NO-OP for the
// ship leg: zero RPC, zero generation, zero bytes. On the response it warms the L2
// cache, marks each id shipped, and advances last-seen generation.
//
// reconcile-on-ship: after the ship-new leg, ship() also PRUNES the
// merged-away segments — the ids in shippedIDs the engine no longer Exports
// (what a background merge consolidated away). Ship-new runs FIRST so the
// consolidated blob lands before its constituents are pruned (never a server
// gap), then Prune removes the stale ids. Both legs are independently zero-RPC
// when there is nothing to do (steady state with no merge → Export == shippedIDs
// → nothing new to ship AND nothing to prune).
//
// RETURN: the set of superseded segment ids reconcilePrune dropped server-side
// (the merged-away ids). The deterministic rebuild path propagates these up to
// Manager.FlushDeterministic → the driver → InvalidateLocal so the local L2
// .seg files for the superseded ids are evicted (they would otherwise orphan
// until LRU, which never fires on an unbounded cache). Every other (embed/
// migration) caller discards the slice — the server-side prune is the behavior
// they already had.
func (m *distManager[Q, S]) ship(ctx context.Context) ([]searchengine.SegmentID, error) {
	if err := m.ensureShippedSeeded(ctx); err != nil {
		return nil, err
	}

	all := m.engine.Export()

	m.shipMu.Lock()
	var diff []*knowledgev1.SegmentBlobProto
	diffBlobs := make(map[string]searchengine.SegmentBlob)
	for _, b := range all {
		if _, sent := m.shippedIDs[b.ID]; sent {
			continue
		}
		diff = append(diff, blobToProto(b))
		diffBlobs[b.ID] = b
	}
	m.shipMu.Unlock()

	// Ship-new FIRST (skips the RPC when the diff is empty but does NOT return —
	// the prune leg below must still reconcile a merge whose consolidated blob was
	// shipped on an earlier pass).
	if err := m.shipNew(ctx, diff, diffBlobs); err != nil {
		return nil, err
	}

	// reconcile-on-ship prune: drop the merged-away ids the server still holds,
	// returning the pruned id set up the stack.
	return m.reconcilePrune(all)
}

// shipNew ships the new-content-hash blobs in one batched Ship, warms the L2
// cache from the stamped response, marks each id shipped, and advances last-seen
// generation. An empty diff is a NO-OP: zero RPC, zero generation, zero bytes.
func (m *distManager[Q, S]) shipNew(
	ctx context.Context,
	diff []*knowledgev1.SegmentBlobProto,
	diffBlobs map[string]searchengine.SegmentBlob,
) error {
	if len(diff) == 0 {
		return nil
	}

	resp, err := m.source.caller.Ship(ctx, &knowledgev1.ShipRequest{
		Target: m.target,
		Blobs:  diff,
	})
	if err != nil {
		return err
	}

	m.shipMu.Lock()
	var maxGen uint64
	for _, meta := range resp.GetStamped() {
		m.shippedIDs[meta.GetId()] = struct{}{}
		if b, ok := diffBlobs[meta.GetId()]; ok {
			m.cache.Put(meta.GetId(), b.Bytes)
		}
		if meta.GetGeneration() > maxGen {
			maxGen = meta.GetGeneration()
		}
	}
	m.shipMu.Unlock()

	m.advanceGen(maxGen)
	return nil
}

// reconcilePrune is the INVERSE of the ship-new diff: it computes the set of ids
// the manager still believes are shipped but the engine no longer Exports — i.e.
// the small segments a background merge consolidated away (searchengine merges
// engine-internal with no callback, so the merged-away set is re-derived here from
// the Export/shippedIDs delta the manager already holds). It Prunes them on the
// server so the server segment set stays BOUNDED across repeated ship+merge cycles,
// then drops them from shippedIDs. Empty pruneSet → ZERO Prune RPC (mirrors the
// empty-diff zero-Ship fast path). Runs under the already-held shipMu lifecycle —
// no new lock; the O(shippedIDs) walk is cheap.
//
// RETURN: the pruneSet it computed + dropped (nil on the empty fast path). ship()
// surfaces this up the stack so the deterministic rebuild path can invalidate the
// superseded ids from the local L2 cache.
func (m *distManager[Q, S]) reconcilePrune(all []searchengine.SegmentBlob) ([]searchengine.SegmentID, error) {
	exportedIDs := make(map[searchengine.SegmentID]struct{}, len(all))
	for _, b := range all {
		exportedIDs[b.ID] = struct{}{}
	}

	m.shipMu.Lock()
	var pruneSet []searchengine.SegmentID
	for id := range m.shippedIDs {
		if _, live := exportedIDs[id]; !live {
			pruneSet = append(pruneSet, id)
		}
	}
	m.shipMu.Unlock()

	if len(pruneSet) == 0 {
		return nil, nil
	}

	if _, err := m.source.Prune(pruneSet); err != nil {
		return nil, err
	}

	m.shipMu.Lock()
	for _, id := range pruneSet {
		delete(m.shippedIDs, id)
	}
	m.shipMu.Unlock()
	return pruneSet, nil
}

// load pulls the server's delta (generation > lastSeenGen) into the engine:
// List the delta, serve cache HITS locally (skip network), batch-Fetch the
// MISSES, warm the cache, and Import the full set. Advances lastSeenGen to the
// max generation in the delta. tombstones is empty — tombstone sourcing is the
// overlay/migration ticket's concern, not this layer.
func (m *distManager[Q, S]) load(ctx context.Context) error {
	listed, err := m.source.List(ctx, m.lastSeenGen.Load())
	if err != nil {
		return err
	}
	// The server bucket holds BOTH formats for this graph (no format dimension in
	// the graphKey); keep only this engine's format so we never decode a foreign
	// blob. Track the max generation across the FULL listed delta (incl. dropped
	// foreign-format metas) so lastSeenGen still advances past them and a later
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
		m.advanceGen(listedMaxGen)
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

	// One batched Fetch for all misses.
	if len(missIDs) > 0 {
		blobs, err := m.source.Fetch(missIDs)
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

	blobs := make([]searchengine.SegmentBlob, 0, len(pend))
	var maxGen uint64
	for _, p := range pend {
		if p.bytes == nil {
			continue // server reported a meta it could not Fetch — skip
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
	// Advance past the WHOLE listed delta (incl. dropped foreign-format metas) so
	// a later load does not re-list and re-drop them.
	if listedMaxGen > maxGen {
		maxGen = listedMaxGen
	}
	m.advanceGen(maxGen)
	return nil
}

// unloadUnderPressure drops resident segments (lowest generation first) via
// engine.Unload until the approximate resident-byte total is at or below target.
// Returns the ids it unloaded so the caller can reload them later. The L2 cache
// retains the bytes, so reload is a cache hit.
func (m *distManager[Q, S]) unloadUnderPressure(targetResidentBytes int) []searchengine.SegmentID {
	m.resMu.Lock()
	defer m.resMu.Unlock()

	type res struct {
		id  searchengine.SegmentID
		seg residentSeg
	}
	ordered := make([]res, 0, len(m.resident))
	total := 0
	for id, seg := range m.resident {
		ordered = append(ordered, res{id: id, seg: seg})
		total += seg.bytes
	}
	// Lowest generation = oldest = evict first.
	sort.Slice(ordered, func(i, j int) bool {
		return ordered[i].seg.generation < ordered[j].seg.generation
	})

	var unloaded []searchengine.SegmentID
	for _, r := range ordered {
		if total <= targetResidentBytes {
			break
		}
		m.engine.Unload([]searchengine.SegmentID{r.id})
		delete(m.resident, r.id)
		total -= r.seg.bytes
		unloaded = append(unloaded, r.id)
	}
	return unloaded
}

// reload re-materializes previously-unloaded segments: L2 cache HIT (no network)
// or Source.Fetch on a miss, then engine.Import. Re-adds them to resident
// tracking.
func (m *distManager[Q, S]) reload(ids []searchengine.SegmentID) error {
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
		fetched, err := m.source.Fetch(missIDs)
		if err != nil {
			return err
		}
		for _, b := range fetched {
			cached[b.ID] = b.Bytes
			m.cache.Put(b.ID, b.Bytes)
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

// advanceGen monotonically raises lastSeenGen to gen (never lowers it).
func (m *distManager[Q, S]) advanceGen(gen uint64) {
	for {
		cur := m.lastSeenGen.Load()
		if gen <= cur || m.lastSeenGen.CompareAndSwap(cur, gen) {
			return
		}
	}
}
