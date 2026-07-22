// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
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

// distManager ties one graph's searchengine.SegmentedIndex to its segmentSource
// (the cloud GCS source when logged in, the local L2-only source otherwise): it
// SHIPS newly-built segments (diffing against what the source already holds),
// LAZILY LOADS the source's delta into the engine (cache-first), and UNLOADS /
// RELOADS resident segments to bound memory. It is generic over the
// engine's [Q, S] format parameters so it works against ANY SegmentFormat (the
// mock format in tests; the real HNSW/BM25 formats once the migration wires the
// engine into client search).
type distManager[Q, S any] struct {
	engine *searchengine.SegmentedIndex[Q, S]
	source segmentSource
	cache  segmentL2Cache
	target *knowledgev1.GraphSelector

	// format is this engine's segment format name (e.g. "hnsw", "bm25"). Each
	// segment source is scoped to one (graph, format) — the cloud source reads a
	// per-(graph, format) agent manifest and the local L2 cache is rooted per-format
	// (graphCacheDirFor) — so a source's List/Fetch returns only THIS engine's
	// format. keepFormat stays as the defensive guard: importing an other-format
	// blob into this engine (a BM25 blob into the HNSW engine, or vice versa) makes
	// Decode fail ("unsupported binary hnsw serial version"), so any stray
	// cross-format blob is dropped rather than imported. An empty format means "no
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
	seeded bool
	// publishPending is the shipMu-guarded republish-retry bit: SET when shipped
	// content changed but publishResident did not complete a successful
	// PublishManifest (coverage-read List error, coverage-gate skip, 409
	// manifestIncompleteError skip, or transport error), CLEARED on PublishManifest
	// success. It rides existing pipeline ticks — the embed gate re-attempts the
	// publish while it is set even when hasUnshippedExport() is false (ship stamped
	// the ids but the publish never landed).
	publishPending bool
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

	// l2Authoritative is true IFF this manager's source is the OSS-local L2-only
	// source (localSegmentSource) — i.e. the not-logged-in/OSS path where there is no
	// cloud segment registry. Derived once from the source type at construction (a
	// localSegmentSource ⟺ l2Authoritative), so the flag and the source impl are
	// provably consistent. It is the single lever the load/degeneracy/reclaim paths
	// branch on to switch the server-fallback legs OFF: L2-only load() (the cold-L2
	// server-Fetch fallback becomes unreachable), the OSS degeneracy collapse
	// (resident-vs-embedded, no server presence probe), and the local-L2 reclaim.
	// the cloud (gcs) and test-fake sources leave it false, preserving the cloud
	// path unchanged.
	l2Authoritative bool
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
	source segmentSource,
	cache segmentL2Cache,
	target *knowledgev1.GraphSelector,
	format string,
) *distManager[Q, S] {
	// Derive l2Authoritative from the source TYPE (no signature change → none of the
	// newDistManager callers change): a localSegmentSource is the OSS-local L2-only
	// source; every other source (the cloud GCS source, the fail-loud sentinel, or a
	// test fake) is not, so it leaves l2Authoritative false. This keeps the flag and
	// the source impl provably consistent.
	_, isLocal := source.(*localSegmentSource)
	return &distManager[Q, S]{
		engine:          engine,
		source:          source,
		cache:           cache,
		target:          target,
		format:          format,
		l2Authoritative: isLocal,
		shippedIDs:      make(map[searchengine.SegmentID]struct{}),
		locallyShipped:  make(map[searchengine.SegmentID]struct{}),
		resident:        make(map[searchengine.SegmentID]residentSeg),
	}
}

// keepFormat reports whether a blob/meta tagged f belongs to this engine's
// format. An empty distManager.format disables the filter (test mock format).
func (m *distManager[Q, S]) keepFormat(f string) bool {
	return m.format == "" || f == m.format
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
