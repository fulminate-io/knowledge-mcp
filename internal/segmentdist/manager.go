// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"sort"
	"sync"
	"sync/atomic"

	"connectrpc.com/connect"

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
	// server. SEEDED ONCE (seedOnce) from Source.List(0) — the server is the
	// single source of truth for what has been shipped; the client RE-DERIVES
	// rather than persisting a drift-prone local file. Guarded by shipMu. Serves
	// TWO purposes: ship-new DIFF suppression (skip re-uploading the seeded
	// corpus), and the ROLE-A authoritative replace-prune used by the
	// deterministic rebuild (FlushDeterministic), whose Export() IS the complete
	// new corpus.
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
	shipMu         sync.Mutex
	seedOnce       sync.Once
	seedErr        error
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

// ensureShippedSeeded lazily seeds shippedIDs from the server's current segment
// set (Source.List(0)) so a fresh process does not re-ship the entire corpus on
// the first ship(). The server is the source of truth; the client re-derives.
// Backed by the idempotent server Put, this seed is an optimization (avoid the
// upload), not a correctness requirement.
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
	m.seedOnce.Do(func() {
		metas, err := m.source.List(ctx, 0)
		if err != nil {
			m.seedErr = err
			return
		}
		m.shipMu.Lock()
		for _, meta := range metas {
			if !m.keepFormat(meta.Format) {
				continue
			}
			m.shippedIDs[meta.ID] = struct{}{}
		}
		m.shipMu.Unlock()
	})
	return m.seedErr
}

// load pulls the server's delta (generation > importedGen) into the engine:
// List the delta, serve cache HITS locally (skip network), batch-Fetch the
// MISSES, warm the cache, and Import the full set. Advances importedGen (the LOAD
// floor — NOT shippedGen) to the max generation in the delta. A cold process has
// importedGen==0, so List(0) returns the full stored corpus and imports it all;
// re-listing this process's own shipped tail is harmless because Import is
// idempotent by segment id. tombstones is empty — tombstone sourcing is the
// overlay/migration ticket's concern, not this layer.
func (m *distManager[Q, S]) load(ctx context.Context) error {
	listed, err := m.source.List(ctx, m.importedGen.Load())
	if err != nil {
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
	// unbounded Fetch(allMisses) — the 2026-06-19 OOM. All-or-hard-error: a byte
	// ceiling that a single blob cannot satisfy returns here BEFORE the Import +
	// importedGen advance below, so the unfetched id stays re-listable.
	if len(missIDs) > 0 {
		blobs, err := m.fetchMisses(missIDs)
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
	// Advance the LOAD floor (importedGen) past the WHOLE listed delta (incl.
	// dropped foreign-format metas) so a later load does not re-list and re-drop
	// them.
	if listedMaxGen > maxGen {
		maxGen = listedMaxGen
	}
	m.advanceGen(&m.importedGen, maxGen)
	return nil
}

// fetchMisses Fetches the named segment ids from the source in COUNT-capped
// sub-batches (at most maxFetchSegmentIDs ids per RPC) and concatenates the
// results, so a cold load never issues one unbounded Fetch(allMisses) (the
// 2026-06-19 OOM). It is the single shared Fetch path for both load and reload.
//
// ADAPTIVE HALVING: a chunk is count-capped, but blobs have no client-visible
// byte size, so a count-capped chunk can still exceed the server's
// store.MaxSegmentFetchResponseBytes byte ceiling. When the server rejects a
// chunk with connect.CodeResourceExhausted (the byte-ceiling backstop maps
// store.ErrSegmentFetchTooLarge to that code), fetchMisses HALVES the chunk and
// retries each half, recursing until each sub-chunk fits under the ceiling. Only
// CodeResourceExhausted triggers halving; ANY OTHER error propagates immediately
// with no retry.
//
// ALL-OR-HARD-ERROR: if a SINGLE id's blob alone exceeds the server ceiling
// (pathological — one segment > MaxSegmentFetchResponseBytes), halving a 1-id
// chunk cannot make it fit, so fetchMisses returns a hard error rather than
// looping forever. That error propagates to load()/reload() BEFORE any Import or
// importedGen advance, so the id stays re-listable on the next load — no silent
// blob loss. fetchMisses returns every requested blob, or an error; never a
// partial set silently.
//
// BACKPRESSURE COUPLING (load-bearing assumption): halving keys on
// CodeResourceExhausted, but a server may have a SECOND source of that code — a
// backpressure mechanism that sheds DB-heavy RPCs with CodeResourceExhausted
// meaning "server busy, back off and retry the SAME batch" (the semantic
// OPPOSITE of "batch too big, halve it"). This is SAFE TODAY because the segment
// Fetch RPC is NOT subject to that backpressure shedding — the byte ceiling is
// the sole ResourceExhausted source on Fetch. If a future change makes Fetch
// subject to backpressure, this code MUST disambiguate the byte-ceiling error
// from a backpressure shed (e.g. via a distinguishable detail on
// ErrSegmentFetchTooLarge) BEFORE halving. (graphclient.IsRetryableTransportError
// deliberately does NOT retry ResourceExhausted, so the segment-level halving
// sees the code cleanly with no double-retry interference.)
func (m *distManager[Q, S]) fetchMisses(missIDs []searchengine.SegmentID) ([]searchengine.SegmentBlob, error) {
	if len(missIDs) == 0 {
		return nil, nil
	}
	out := make([]searchengine.SegmentBlob, 0, len(missIDs))
	for start := 0; start < len(missIDs); start += maxFetchSegmentIDs {
		end := min(start+maxFetchSegmentIDs, len(missIDs))
		blobs, err := m.fetchChunkAdaptive(missIDs[start:end])
		if err != nil {
			return nil, err
		}
		out = append(out, blobs...)
	}
	return out, nil
}

// fetchChunkAdaptive Fetches one already-count-capped chunk, halving it on a
// server byte-ceiling rejection (CodeResourceExhausted) until each sub-chunk
// fits. A 1-id chunk that still exceeds the ceiling is a hard error (no infinite
// loop). Any non-ResourceExhausted error propagates immediately. See fetchMisses
// for the full halving + backpressure-coupling rationale.
func (m *distManager[Q, S]) fetchChunkAdaptive(chunk []searchengine.SegmentID) ([]searchengine.SegmentBlob, error) {
	blobs, err := m.source.Fetch(chunk)
	if err == nil {
		return blobs, nil
	}
	if connect.CodeOf(err) != connect.CodeResourceExhausted {
		return nil, err // not a byte-ceiling rejection — propagate, no retry
	}
	// Byte ceiling: the chunk is too large in bytes despite being count-capped.
	// A single id that still over-runs the ceiling cannot be split further.
	if len(chunk) <= 1 {
		return nil, err
	}
	mid := len(chunk) / 2
	left, err := m.fetchChunkAdaptive(chunk[:mid])
	if err != nil {
		return nil, err
	}
	right, err := m.fetchChunkAdaptive(chunk[mid:])
	if err != nil {
		return nil, err
	}
	return append(left, right...), nil
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
		// Sub-batched Fetch (count-capped + byte-ceiling halving) so a large
		// reload never issues one unbounded Fetch(allMisses).
		fetched, err := m.fetchMisses(missIDs)
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
