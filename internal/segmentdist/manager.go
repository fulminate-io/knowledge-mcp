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
	// GetMapped is the resident read path's variant: it returns the blob as a
	// memory mapping plus its release closure, so the bytes live in the OS page
	// cache instead of the Go heap. A non-nil error means the id IS cached but
	// could not be mapped, which callers surface rather than treat as a miss.
	GetMapped(id searchengine.SegmentID) (data []byte, release func(), ok bool, err error)
	Put(id searchengine.SegmentID, b []byte)
	Remove(id searchengine.SegmentID)
	// Keys enumerates the L2-resident segment ids server-independently so load()
	// can reconstruct the resident set from L2 alone when the server manifest is
	// unavailable (slow/down server). It reads only the in-memory index — no disk
	// re-read, no network.
	Keys() []searchengine.SegmentID
	// sizeOf reports one id's stored byte size and whether it is L2-resident at
	// all. It is the eviction re-materializability gate's probe (evictResident,
	// manager_residency.go): Get would read the whole file back off disk and
	// MoveToFront the LRU, so gating on Get would turn a memory reclaim into a
	// disk-read storm AND perturb the very recency ordering the budget sorts on.
	// sizeOf reads the in-memory index only and is recency-neutral.
	sizeOf(id searchengine.SegmentID) (int64, bool)
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

	// format is this engine's segment format name, as the format itself reports it. Each
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
	// (FinalizeRebuild), whose Export() IS the complete new corpus.
	//
	// locallyShipped is the set of ids THIS PROCESS shipped via shipNew — seeded
	// EMPTY and never populated from the server. It is the ROLE-B prune-eligible
	// set: the embed/tail ship path (ReEmitDirtyBuckets/Flush) reconciles
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
	// coverageSkipStreak + lastSkipResident bound the publishPending re-arm on the
	// ONE self-sustaining cause: the coverage-ratio skip. markCoverageSkip (the
	// replacement for the coverage-skip setPublishPending call) counts consecutive
	// skips at a NON-RISING resident and stops re-arming once the streak passes
	// coverageSkipMaxStreak — the read engine is stuck below the ratio, so retrying
	// only re-reads the SAME sub-ratio resident and re-skips. lastSkipResident is the
	// resident doc count at the last skip; a rise above it (genuine progress) resets
	// the streak so a healing engine re-arms. Both guarded by shipMu; both cleared on
	// a successful PublishManifest alongside publishPending. Deliberately asymmetric
	// with the bootstrap heal breaker, which latches until a manual op/restart — this
	// auto-re-arms on a resident rise.
	coverageSkipStreak int
	lastSkipResident   int
	// coverageSuppressedAtNanos records WHEN this engine's coverage gate became
	// unsatisfiable: a time.Now().UnixNano() stamped on the streak's TRANSITION into
	// suppression and cleared everywhere the streak is (a resident rise in
	// markCoverageSkip, a landed manifest swap in publishResident), so it is non-zero
	// exactly while the engine sits in a suppression episode. It is stamped on the
	// EDGE rather than on every suppressing skip because the age it feeds measures
	// from the moment retrying stopped being able to help, and a re-stamp per skip
	// would keep resetting that age to now. Guarded by shipMu with the streak fields
	// it accompanies. Per-process like all of them: a restart clears it, so the age
	// is "how long in THIS process".
	coverageSuppressedAtNanos int64
	// incompletePublishStreak counts CONSECUTIVE agent-409 (manifestIncompleteError)
	// publish skips for this engine, reset to 0 on a landed swap. Unlike the
	// coverage-skip streak it does NOT bound the retry re-arm — the 409 cause is meant
	// to self-heal, because markIncompletePublish un-stamps the missing ids so the next
	// ship diff RE-UPLOADS them. Its sole job is to distinguish a transient 409 (heals
	// within a cycle) from a PERSISTENT one (the re-upload is not sticking): once the
	// streak reaches incompletePublishWarnStreak, markIncompletePublish escalates the
	// per-cycle transient WARN to a loud degradation WARN. Guarded by shipMu; cleared on
	// a successful PublishManifest alongside publishPending and the coverage-skip fields.
	incompletePublishStreak int
	// completedSwaps counts the manifest swaps that actually LANDED — incremented
	// beside the publishPending clear, on the one path where PublishManifest
	// returned success. It exists because a nil error does NOT mean a publish
	// happened: publishResident returns (nil, nil) on the coverage-gate skip and on
	// the agent 409, so a caller that needs to know a swap COMPLETED (the rebuild
	// driver, which advances a durable watermark only then) cannot read the error.
	// Reading the counter across a call and comparing is that signal. Guarded by
	// shipMu, like the publishPending bit it is the completion counterpart of.
	completedSwaps uint64
	shippedIDs     map[searchengine.SegmentID]struct{}
	locallyShipped map[searchengine.SegmentID]struct{}

	// onCoverageSuppressed is the nil-safe hook markCoverageSkip fires ONCE per
	// suppression episode, on the streak's transition into suppression — the point
	// at which retrying the publish can no longer help and only an outside event
	// (a resident rise) can clear it. The Manager wires it at construction
	// (manager_factory.go) to record this graph in its reconcile-nudge set; it is
	// nil for a directly-constructed distManager, hence the nil check at the call
	// site. Assigned once, before the manager is reachable by any other goroutine,
	// and only READ afterwards — so unlike the streak fields above it needs no
	// lock, and it is deliberately invoked OUTSIDE shipMu.
	onCoverageSuppressed func()

	// onManifestPublished records the fingerprint of a manifest swap that LANDED, so
	// the off-hot-path completeness reconcile has a cheap local number to compare
	// len(cache.Keys()) against without reading the server every tick
	// (manager_completeness.go). Fired from publishResident — the ONE function that
	// completes a swap — immediately beside the completedSwaps increment, so the
	// record and the counter can never disagree about what landed.
	//
	// The Manager wires it at construction (manager_factory.go); it is nil for a
	// directly-constructed test distManager, hence the nil check at the call site.
	// Same assign-once-then-read-only lifetime as onCoverageSuppressed, so it needs
	// no lock.
	onManifestPublished func(ids []searchengine.SegmentID)

	// tombstoneSeed supplies the ids every Import must mark dead in the segments it
	// imports, so a blob shipped before a delete cannot resurrect the removed node.
	// It is a SUPPLIER rather than a stored slice because the owner's set changes as
	// deletes are learned and re-emitted, and an engine holding its own copy would
	// drift. nil-safe: a nil hook means no seeding, which is the correct behavior
	// for the test engines that construct a distManager directly.
	tombstoneSeed func() []searchengine.ExternalID

	// resident tracks the segments currently imported into the engine + an
	// approximate resident-byte total (sum of imported blob byte lengths). Guarded
	// by resMu. unloaded holds the bytes of segments dropped under pressure so
	// reload can re-Import from L2 without a network round-trip.
	resMu    sync.Mutex
	resident map[searchengine.SegmentID]residentSeg

	// evictedIDs is the EXACT id set the last evictResident unloaded, recorded so
	// the re-materialization is a STRICT reload of that set (manager_residency.go,
	// load()'s evicted branch) rather than a tolerant re-derive from cache.Keys(),
	// which would silently serve a SHORT hit list. Guarded by resMu, beside
	// resident.
	//
	// It has EXACTLY TWO writers, with different jobs, and neither does the
	// other's: markMaterialized DROPS it (the pool is resident again, latch
	// cleared), and reclaimMerged REWRITES it while the latch is still SET (drop
	// res.Removed, add res.Merged.ID) so a merge completing after eviction cannot
	// make the strict reload hard-error on data that is perfectly intact. A third
	// writer is a defect.
	evictedIDs []searchengine.SegmentID

	// remapPending holds the segments whose mapping republication has not yet
	// succeeded, keyed by segment id and guarded by resMu — the SAME lock that
	// already guards resident and evictedIDs, because this set has the same
	// lifecycle as those and a separate lock would create an ordering question
	// resMu's existing discipline (residencyMu before resMu, per evictResident)
	// already answers.
	//
	// A pending entry is a DEGRADED-BUT-CORRECT state, not a lost one: the
	// previous heap-backed payload is still published and still serves the same
	// results. Only the memory property is missing, which is precisely why the
	// condition used to be logged and forgotten — the damage is invisible at
	// every gate. Recording it is what makes the repair convergent instead.
	remapPending map[searchengine.SegmentID]remapAttempt

	// lastSearchNanos is the last CONSUMER-SEARCH touch stamp (time.Now().UnixNano),
	// written by noteSearchTouch and read by lastSearchTouch. It defines hot/cold for
	// the residency budget, and it is stamped by the SEARCH path only: the reconcile,
	// coverage-probe and rebuild arms run against the whole working set hourly, so
	// counting them as touches would keep every pool permanently hot and defeat
	// eviction entirely. A never-searched pool reads 0 and is therefore the coldest.
	lastSearchNanos atomic.Int64

	// evicted latches true while this pool's segments have been unloaded to reclaim
	// memory and have not yet been re-materialized. It is what makes an evicted pool
	// DISTINGUISHABLE to a background arm (which must decline rather than resurrect
	// it) while staying INDISTINGUISHABLE to a searcher (whose load() transparently
	// re-materializes it). markMaterialized is the SINGLE owner of its clear.
	// Modeled on l2Loaded/recovering: a lock-free atomic.Bool.
	evicted atomic.Bool

	// residencyMu serializes eviction against the consumer load-and-search span.
	// engine.Unload's CAS swap is tear-safe on its own (searchengine/segmentset.go
	// declares segmentSet an immutable snapshot, and Search does one set.Load for
	// the whole call), but a CONSUMER calls load() and engine.Search as two separate
	// statements — an eviction landing between them leaves Search reading an empty
	// snapshot, which is a silent miss no property of the CAS prevents. Consumers
	// hold RLock across the whole load+Search span; evictResident holds Lock for its
	// whole body. sync.RWMutex is NOT reentrant, so nothing called while an RLock is
	// held may take Lock — see markMaterialized.
	residencyMu sync.RWMutex

	// recovering single-flights the read-side degeneracy backstop (recoverIfDegenerate
	// in manager_backstop.go): the FIRST search to find a degenerate engine CASes it
	// true, resets the load floor, and re-imports the corpus; concurrent searches see
	// it already set and skip (the recovery will make the corpus resident shortly).
	recovering atomic.Bool

	// coverageMemo caches the PUBLISH-path shipped denominator ONLY — the result of
	// this manager's shippedDocCountForRatio read inside publishCoverageOK — so a
	// coverage-skip storm stops paying a List round-trip per attempt. Read the LIMIT
	// with the coverage: it is invalidated by THIS manager's own ship and its own
	// successful publish, and those hooks do NOT cover the deterministic-rebuild
	// manager, which shares this manager's manifest but not its memo and is therefore
	// observed only at TTL expiry (shippedDocCountForRatioCached carries the full
	// reasoning, including why a memo-derived PASS is always re-derived before it is
	// honored). It is never consulted by the read-side backstop (recoverIfDegenerate)
	// or the reconcile probe (ReconcileResidentDegenerate) — both keep reading fresh.
	coverageMemo atomic.Pointer[coverageDenominator]

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
	// mappedBytes is the ENCODED blob length of this segment. It is metered and
	// reported, never compared against the residency budget: for a mapped
	// segment these bytes are page cache rather than Go heap, so the budget
	// reads the engine's modeled heap instead (see residentBytes).
	mappedBytes int
	format      string
	generation  uint64
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
		remapPending:    make(map[searchengine.SegmentID]remapAttempt),
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
