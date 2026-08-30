// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"log/slog"
	"sync"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine/formats/bm25"
)

// Manager is the PRODUCTION owner of per-graph HNSW segment engines. It is the
// production constructor + per-graph routing layer over the distManager: one
// searchengine.SegmentedIndex[[]byte, struct{}] over the HNSW format per
// (graphType, graphName), lazily constructed, each wired to its segment source (a
// GCS-agent source when logged in, an L2-only local source otherwise) + an L2
// diskSegmentCache.
//
// The client builds + ships HNSW segments from the binary vectors it already
// holds at the pipeline embed-writeback seam. AddAndMarkDirty is the HNSW write
// entry point; AddAndMarkDirtyFields is the BM25 one; both seal without shipping
// and leave durability to ReEmitDirtyBuckets on the reconcile tick. ONE Manager
// owns BOTH formats per graph — two per-format per-graph maps, each lazily
// constructed and rooted under a format-distinct L2 cache directory so they never
// collide.
type Manager struct {
	cacheDir string
	maxBytes int64
	// residencyBudgetBytes is the ceiling, in RESIDENT HEAP BYTES, that every
	// constructed pool's imported segments may occupy together. Crossing it evicts
	// the coldest pools until the total is back under (manager_residency.go).
	// ZERO DISABLES EVICTION ENTIRELY and is the default, so a Manager built without
	// WithResidencyBudget behaves exactly as it did before the budget existed.
	// Written once at construction and only read afterwards, like boundAccountID.
	//
	// IT IS A DIFFERENT QUANTITY FROM maxBytes ABOVE and the two are never
	// interchangeable: maxBytes bounds .seg FILES ON DISK (the L2 cache cap),
	// residencyBudgetBytes bounds DECODED SEGMENTS IN RAM. Evicting under this
	// budget deliberately leaves the disk files alone — that is exactly what makes
	// the reload free of network.
	residencyBudgetBytes int64
	// boundAccountID is the Fulminate account selected when this Manager was
	// constructed — the account its cacheDir and its per-graph sources belong
	// to. Every serving entry point compares it against the live selection and
	// REFUSES on a mismatch (manager_account_guard.go) rather than serving the
	// previous account's cached segments. Written once at construction.
	boundAccountID string

	mu sync.Mutex
	// managers holds the HNSW engine per graph (vectors). bm25Managers holds the
	// BM25 engine per graph (field-bearing text). Both guarded by mu, and both
	// hold a constructionGate rather than the engine directly, so a caller that
	// races a branch seed waits for it instead of observing a half-filled engine.
	managers     map[graphKey]*constructionGate[[]byte, struct{}]
	bm25Managers map[graphKey]*constructionGate[bm25.Query, *bm25.CorpusStats]
	// ONE ENGINE PER FORMAT, and the pair above is the whole set. A third map used to
	// hold a DETERMINISTIC HNSW engine the rebuild wrote, plus a fourth holding the
	// outgoing layer it had to pin while that engine was dropped — because both HNSW
	// engines keyed ONE (graphKey, writerID, HNSW) manifest, so an embed publish
	// landing mid-rebuild could name a manifest omitting the layer still serving the
	// graph and reap the live corpus. A reset now builds aside and swaps at the serving
	// engine, so the engine goes on serving the old layer right up to the CAS and there
	// is no unreferenced window to pin across.

	// rebuildWork stages a reset rebuild's partitions — BOTH formats — until its
	// finalize. StageRebuildPartition appends ONE entry per format per call; the driver
	// already groups the scan by bucket and calls once per group, so the staged lists
	// ARE its grouping. FinalizeRebuild takes them ONCE and builds each format's whole
	// layer aside, ships it, and swaps it in atomically.
	//
	// IT IS STAGED RATHER THAN ADDED because a rebuild must not touch the corpus it is
	// replacing until the replacement is ready. Adding partitions incrementally
	// pollutes the SERVING engine one partition at a time and leaves the finalize
	// publishing the union of every layer that engine has ever held — measured live as
	// three bm25 blobs where one was correct, and as an hnsw manifest holding twice its
	// corpus. Staging keeps the serving engines untouched until one swap per format
	// replaces each layer whole. Guarded by mu.
	rebuildWork map[graphKey]*stagedRebuild

	// seedHook, when non-nil, runs at two named points of the branch seed. It is
	// TEST-ONLY (set via withSeedHook) and nil in every production Manager.
	seedHook func(phase seedPhase, gt kgtypes.GraphType, name, format string)

	// admitGraph records that a user searched a graph, admitting it into the
	// client's working set. nil when no admitter was supplied, in which case a
	// search records nothing — the same default-deny direction the working set
	// itself takes. Set via WithGraphAdmitter.
	admitGraph func(gt kgtypes.GraphType, name string)

	nudges nudgeState // publish-suppression record + coalescing wake — manager_nudge.go.

	// dirty holds the per-graph re-emit backlog the embed drain accumulates and the
	// reconcile tick drains: the documents awaiting a bucket re-emit and the ids of
	// the tail segments those drains sealed. Guarded by mu; see manager_bucket.go.
	dirty map[graphKey]*graphDirtyState

	// writeSeq issues the monotonic sequence every backlog entry is stamped with. It
	// is process-wide rather than per graph: the sequences only ever need to ORDER
	// events against each other, and one counter orders them across every graph for
	// free. Sequences start at 1, so a zero is never a valid entry. Guarded by mu.
	writeSeq uint64

	// mergeStateMu guards the per-graph delta-merge horizon record (merge_state.go),
	// and repairStateMu guards the per-graph backstop record plus its hot map
	// (repair_state.go). Each record has its own writer and its own file, so each
	// takes its own mutex rather than mu: mu guards the per-graph engine maps and is
	// held across lazy engine construction, while these are held only across a small
	// file read+rename. bm25DeltaStateMu guards the per-graph BM25 arm cursor record
	// (bm25_delta_state.go) under the same rationale.
	mergeStateMu     sync.Mutex
	repairStateMu    sync.Mutex
	bm25DeltaStateMu sync.Mutex
	// repairStateHot is what keeps a disk read off the manage(status) assembly loop,
	// which walks every graph serially. LoadRepairState fills it and SaveRepairState
	// updates it; RepairStateCached is a pure map read that never falls back to disk.
	// Lazily created under repairStateMu.
	repairStateHot map[graphKey]RepairState

	// tombstoned holds the ids this client has learned are deleted but which may
	// still appear in blobs shipped BEFORE the delete. Every Import seeds the
	// imported segments' live bits from it, so re-importing such a blob cannot
	// resurrect a removed node. Guarded by mu.
	//
	// IT IS SUPPLIED BY CALLERS AND ALSO HYDRATED FROM DISK, and the second half is not
	// a second source of truth: the durable record is THIS package's
	// (rebuild_state.go's per-graph {watermark, tombstoned} file), every route that
	// learns a delete writes it before seeding, and graphTombstones reads it back once
	// per graph so a fresh process's very first import is masked too.
	tombstoned map[graphKey][]searchengine.ExternalID

	// tombstonesHydrated latches per graph once the durable record has been consulted
	// OR a caller has replaced the set outright. Without it an empty set would re-read
	// the record on every import, and — worse — a deliberate CLEAR would be undone by
	// the next read. Guarded by mu, beside the map it qualifies.
	tombstonesHydrated map[graphKey]bool

	// tombstoneSeq records, PER GRAPH AND PER ID, the write sequence in force when a
	// delete for that id was last reported. It is what turns "this write arrived AFTER
	// the delete" from an inference into a CHECKED FACT.
	//
	// THE INFERENCE IT REPLACES WAS WRONG. Not every route that tombstones an id purges
	// the write backlog: SetGraphTombstones has three production call sites and
	// purgeDirty has one, so the delta consumer's already-known branch and the rebuild
	// driver both seed tombstones with the backlog untouched. Reporting a queued write
	// for such an id would resurrect a STALE document.
	//
	// IT IS PER ID, NOT PER GRAPH, AND THAT GRANULARITY IS THE WHOLE POINT. A per-graph
	// stamp is written on every window that reports any delete at all — the delta
	// consumer's only early return is the zero-tombstone one — and it would therefore
	// suppress a queued re-creation of X merely because an UNRELATED Y was deleted in
	// the same window. That is the exposure this whole mechanism exists to close,
	// defeated by its own fix. Only X's own stamp may gate X.
	//
	// Guarded by mu.
	tombstoneSeq map[graphKey]map[searchengine.ExternalID]uint64
}

// NoteDeletedIDs records that these ids were REPORTED DELETED right now, stamping each
// with the current write sequence. The caller passes the ids the CURRENT window
// actually reported — never the accumulated set — because the accumulated set would
// re-stamp ids whose deletes are old and suppress writes that legitimately followed
// them.
//
// It is separate from SetGraphTombstones on purpose: that one REPLACES the live set
// (and is handed the merged union), this one records WHEN each delete was learned.
// Callers drive both.
func (m *Manager) NoteDeletedIDs(gt kgtypes.GraphType, name string, ids []searchengine.ExternalID) {
	if len(ids) == 0 {
		return
	}
	k := graphKey{graphType: gt, graphName: name}
	m.mu.Lock()
	defer m.mu.Unlock()
	stamps, ok := m.tombstoneSeq[k]
	if !ok {
		stamps = make(map[searchengine.ExternalID]uint64, len(ids))
		m.tombstoneSeq[k] = stamps
	}
	// The highest sequence issued so far, so any write that BEGAN earlier compares as
	// older than this delete.
	for _, id := range ids {
		stamps[id] = m.writeSeq
	}
}

// SetGraphTombstones replaces the tombstone set the graph's engines seed imported
// segments from. The caller owns the record — this only holds what it is given.
//
// WHY A SEED IS NEEDED AT ALL: a delete clears the live bit in memory and the
// bucket re-emit removes the document from the durable blob, but a blob shipped
// BEFORE the delete can still be sitting in L2 or on the server. Importing one
// starts every member LIVE, so without this set that import brings the deleted node
// back into the searchable set until the next re-emit of its partition touches it.
//
// SOFT DELETES ONLY, and the bound is real rather than a caveat. These ids come
// from the tombstone feed. A HARD delete removes the row outright, so it never
// appears there and never enters this set; a hard delete this process performed is
// covered by its own re-emit, and one performed elsewhere reaches this client at the
// next full rebuild.
//
// Passing an empty or nil set clears the graph's entry, which is what a caller does
// once every id has been re-emitted out of the durable blobs.
//
// IT LATCHES hydrated. A caller replacing the set has already decided what the graph's
// tombstones ARE — the two clearing routes both rewrite the persisted record before they
// call here — so a later read must not go back to disk and re-learn ids this call
// deliberately dropped.
func (m *Manager) SetGraphTombstones(gt kgtypes.GraphType, name string, ids []searchengine.ExternalID) {
	k := graphKey{graphType: gt, graphName: name}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tombstonesHydrated[k] = true
	if len(ids) == 0 {
		// Nothing is left to seed dead, so nothing is left to time either.
		delete(m.tombstoned, k)
		delete(m.tombstoneSeq, k)
		return
	}
	m.tombstoned[k] = append([]searchengine.ExternalID(nil), ids...)
}

// nextWriteSeq issues the next write sequence. IT TAKES mu ITSELF; callers must NOT
// hold it.
//
// The write entry points call this BEFORE they seal, so the value upper-bounds when
// the write BEGAN rather than when it finished being recorded.
func (m *Manager) nextWriteSeq() uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.writeSeq++
	return m.writeSeq
}

// graphTombstones returns a snapshot of the graph's tombstone set for an Import to
// seed from. It copies under the lock so a concurrent SetGraphTombstones cannot
// mutate the slice an in-flight import is reading.
//
// IT HYDRATES FROM THE DURABLE RECORD ON THE FIRST READ, and without that a restart has
// no seal at all: the in-memory set starts empty in every fresh process, while the very
// first thing that process does with a graph is import its whole L2 corpus — blobs that
// may predate the deletes this client already learned. The record is this package's own
// (rebuild_state.go), written by every route that learns a delete, so reading it back
// here is a read of the same fact rather than a second opinion about it.
//
// ONCE PER GRAPH, NOT ONCE PER IMPORT. hydrated latches on the first read AND on every
// SetGraphTombstones, so the disk is touched once and an authoritative CLEAR is never
// undone by a re-read of a record the clearing caller has already rewritten.
func (m *Manager) graphTombstones(gt kgtypes.GraphType, name string) []searchengine.ExternalID {
	k := graphKey{graphType: gt, graphName: name}
	m.mu.Lock()
	defer m.mu.Unlock()
	ids := m.tombstoned[k]
	if len(ids) == 0 {
		ids = m.hydrateTombstonesLocked(k, gt, name)
	}
	if len(ids) == 0 {
		return nil
	}
	return append([]searchengine.ExternalID(nil), ids...)
}

// hydrateTombstonesLocked reads the graph's persisted tombstone set into the in-memory
// one, once. Callers must hold mu.
//
// AN UNREADABLE RECORD IS ANNOUNCED, NOT ABSORBED. This runs under a supplier signature
// that cannot return an error — the engines' Import takes a set, not a result — so the
// only honest disposition is a loud one naming the consequence: the import proceeds
// unmasked, which resurrects whatever the record was holding dead. It latches anyway, so
// a broken record produces one ERROR per graph rather than one per import.
func (m *Manager) hydrateTombstonesLocked(
	k graphKey, gt kgtypes.GraphType, name string,
) []searchengine.ExternalID {
	if m.tombstonesHydrated[k] {
		return nil
	}
	m.tombstonesHydrated[k] = true
	_, stored, err := m.LoadRebuildState(gt, name)
	if err != nil {
		slog.Error("segmentdist: the graph's tombstone record is unreadable — segments imported by this process are NOT masked, so a blob shipped before a delete can resurrect its documents",
			"graph", gt, "name", name, "err", err)
		return nil
	}
	if len(stored) == 0 {
		return nil
	}
	m.tombstoned[k] = stored
	return stored
}

// ManagerOption configures a Manager at construction. See the With* functions.
type ManagerOption func(*Manager)

// WithGraphAdmitter supplies the recorder that admits a searched graph into the
// client's working set. A search IS the direct interaction the working-set rule
// names, so the Manager reports it at the same instant it nudges the reconcile
// loop. Without this option a Manager records nothing.
func WithGraphAdmitter(admit func(gt kgtypes.GraphType, name string)) ManagerOption {
	return func(m *Manager) { m.admitGraph = admit }
}

// WithResidencyBudget sets the ceiling, in RESIDENT HEAP BYTES, that the segment
// pools of every constructed graph may occupy together before the coldest of them
// are evicted from memory (manager_residency.go). Without this option — or with a
// budget of zero or less — eviction is DISABLED and residency is unbounded, which
// is the behaviour every caller had before the budget existed.
func WithResidencyBudget(bytes int64) ManagerOption {
	return func(m *Manager) { m.residencyBudgetBytes = bytes }
}

// seedPhase names which point of the branch seed a test hook was called from. The
// two are different windows and a test that could not tell them apart would park or
// act at whichever came first.
type seedPhase string

const (
	// seedPhaseConstruct fires at the top of a branch constructor's seed, OFF the
	// Manager lock — the point where a racing caller is supposed to be made to wait.
	seedPhaseConstruct seedPhase = "construct"
	// seedPhaseRecordCaptured fires inside the seed AFTER base's rebuild record has
	// been read and BEFORE any partition moves — the window in which base advancing
	// its record must not reach the branch.
	seedPhaseRecordCaptured seedPhase = "record-captured"
)

// withSeedHook is the TEST-ONLY option that runs fn at the two named seed phases
// above. It is unexported and never used by production code.
//
// WHY A HOOK AND NOT A BLOCKING DOUBLE. Both windows used to be reachable through a
// segment source: the seed called the source's List between capturing base's record
// and copying its partitions, so a double could park there or act there. The seed is
// now a pure L2 copy and consults no source at all, so a source double is never
// called and either test would hang or assert nothing.
//
// WHY THE CONSTRUCT PHASE IS WHERE IT IS. Every other seam in the constructor runs
// UNDER the Manager lock, and a test that parked there would prove the MUTEX rather
// than the construction gate — a racing caller would block on the lock and observe
// the right answer even with the gate deleted, which is a test that cannot fail. The
// seed is the only work the constructor does off the lock.
func withSeedHook(fn func(phase seedPhase, gt kgtypes.GraphType, name, format string)) ManagerOption {
	return func(m *Manager) { m.seedHook = fn }
}

// stagedRebuild is one graph's in-flight reset: the partitions staged for each
// format, in the order the driver supplied them. The two lists move together because
// they are staged together — one call per bucket carries both formats' share, so a
// caller structurally cannot stage one and forget the other.
type stagedRebuild struct {
	hnsw []searchengine.BucketWork
	bm25 []searchengine.BucketWork
}

// graphKey routes one (graphType, graphName) to its dedicated engine+distManager.
type graphKey struct {
	graphType kgtypes.GraphType
	graphName string
}

// NewManager constructs the production owner. cacheDir roots the per-graph L2 disk
// caches; maxBytes <= 0 means an unbounded cache. There is no login parameter: the
// source factory has one source to select, so the caller's cloud login state is not
// an input to segment distribution at all.
//
// opts are optional construction knobs; see the With* functions.
func NewManager(cacheDir string, maxBytes int64, opts ...ManagerOption) *Manager {
	m := &Manager{
		cacheDir: cacheDir,
		maxBytes: maxBytes,
		// Sampled ONCE here, alongside the cacheDir it belongs to.
		boundAccountID:     accountSelectionID(context.Background()),
		managers:           make(map[graphKey]*constructionGate[[]byte, struct{}]),
		bm25Managers:       make(map[graphKey]*constructionGate[bm25.Query, *bm25.CorpusStats]),
		dirty:              make(map[graphKey]*graphDirtyState),
		tombstoned:         make(map[graphKey][]searchengine.ExternalID),
		tombstonesHydrated: make(map[graphKey]bool),
		tombstoneSeq:       make(map[graphKey]map[searchengine.ExternalID]uint64),
		rebuildWork:        make(map[graphKey]*stagedRebuild),
	}
	for _, opt := range opts {
		opt(m)
	}
	return m
}
