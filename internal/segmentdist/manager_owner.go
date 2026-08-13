// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"sync"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
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
	caller   loginState
	cacheDir string
	maxBytes int64

	mu sync.Mutex
	// managers holds the HNSW engine per graph (vectors). bm25Managers holds the
	// BM25 engine per graph (field-bearing text). Both guarded by mu.
	managers     map[graphKey]*distManager[[]byte, struct{}]
	bm25Managers map[graphKey]*distManager[bm25.Query, *bm25.CorpusStats]
	// ONE ENGINE PER FORMAT, and the pair above is the whole set. A third map used to
	// hold a DETERMINISTIC HNSW engine the rebuild wrote, plus a fourth holding the
	// outgoing layer it had to pin while that engine was dropped — because both HNSW
	// engines keyed ONE (graphKey, writerID, "hnsw") manifest, so an embed publish
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

	// segTransport lazily builds the agent /v1/segments control transport for the
	// cloud (logged-in) segment source. nil when no builder was supplied: on the
	// logged-in branch the source factory then returns the fail-loud
	// errorSegmentSource sentinel (a logged-in client with no/failed transport is
	// misconfigured — it must surface, not silently degrade). Sampled once per lazy
	// per-graph source construction. The production *auth.Transport it returns
	// satisfies the SegmentControlTransport seam; in-package tests inject a fake
	// through the same builder.
	segTransport func() (SegmentControlTransport, error)

	// testSource, when non-nil, is the segmentSource EVERY lazily-constructed
	// distManager uses, bypassing the newSegmentSource capability gate entirely. It
	// is TEST-ONLY (set via withSegmentSource) so the surviving in-package machinery
	// tests inject a fake segment source without threading it through the production
	// login/transport gate. nil in every production Manager.
	testSource segmentSource

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

	// manifestStateMu guards the per-graph manifest-fingerprint record's
	// read-modify-write (manifest_state.go). BOTH format arms of one graph write the
	// same file, so an unguarded pair would lose whichever landed first. It is
	// deliberately NOT mu: mu guards the per-graph engine maps and is held across
	// lazy engine construction, while this is held only across a small file
	// read+rename.
	manifestStateMu sync.Mutex

	// mergeStateMu guards the per-graph delta-merge horizon record (merge_state.go),
	// and repairStateMu guards the per-graph backstop record plus its hot map
	// (repair_state.go). Each record has its own writer and its own file, so each
	// takes its own mutex for the same reason manifestStateMu is not mu: they are
	// held only across a small file read+rename, never across engine construction.
	mergeStateMu  sync.Mutex
	repairStateMu sync.Mutex
	// repairStateHot is what keeps a disk read off the manage(status) assembly loop,
	// which walks every graph serially. LoadRepairState fills it and SaveRepairState
	// updates it; RepairStateCached is a pure map read that never falls back to disk.
	// Lazily created under repairStateMu.
	repairStateHot map[graphKey]RepairState

	// tombstoned holds the ids this client has learned are deleted but which may
	// still appear in blobs shipped BEFORE the delete. Every Import seeds the
	// imported segments' live bits from it, so re-importing such a blob cannot
	// resurrect a removed node. Guarded by mu; supplied by the caller through
	// SetGraphTombstones, never derived here — segmentdist does not know where the
	// set is persisted.
	tombstoned map[graphKey][]searchengine.ExternalID

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
func (m *Manager) SetGraphTombstones(gt kgtypes.GraphType, name string, ids []searchengine.ExternalID) {
	k := graphKey{graphType: gt, graphName: name}
	m.mu.Lock()
	defer m.mu.Unlock()
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
func (m *Manager) graphTombstones(gt kgtypes.GraphType, name string) []searchengine.ExternalID {
	k := graphKey{graphType: gt, graphName: name}
	m.mu.Lock()
	defer m.mu.Unlock()
	ids := m.tombstoned[k]
	if len(ids) == 0 {
		return nil
	}
	return append([]searchengine.ExternalID(nil), ids...)
}

// targetBindable is the seam newSegmentSource uses to re-bind an INJECTED test
// source to the per-graph target, without the production factory referencing a
// test-only concrete type. Only the in-package test fake implements it; production
// sources (gcs/local/error) do not, so the type-assert is a no-op for them.
type targetBindable interface {
	bindTarget(*knowledgev1.GraphSelector)
}

// ManagerOption configures a Manager at construction. See the With* functions.
type ManagerOption func(*Manager)

// WithSegmentTransport supplies the lazy agent /v1/segments control-transport
// builder that selects the cloud (GCS) segment source on the logged-in path. The
// production caller wraps cli.BuildSyncTransport (which returns the *auth.Transport
// satisfying SegmentControlTransport). Without this option a Manager has no transport
// builder, so the logged-in path resolves to the fail-loud errorSegmentSource
// sentinel (a logged-in client with no segment transport is misconfigured).
func WithSegmentTransport(builder func() (SegmentControlTransport, error)) ManagerOption {
	return func(m *Manager) { m.segTransport = builder }
}

// WithGraphAdmitter supplies the recorder that admits a searched graph into the
// client's working set. A search IS the direct interaction the working-set rule
// names, so the Manager reports it at the same instant it nudges the reconcile
// loop. Without this option a Manager records nothing.
func WithGraphAdmitter(admit func(gt kgtypes.GraphType, name string)) ManagerOption {
	return func(m *Manager) { m.admitGraph = admit }
}

// withSegmentSource is the TEST-ONLY option that pins the segmentSource every
// lazily-constructed distManager uses, bypassing the newSegmentSource capability
// gate. The surviving in-package machinery tests inject a fakeSegmentSource through
// it so they exercise the manager over a controllable double without a live
// login/transport. It is unexported and never used by production code.
func withSegmentSource(src segmentSource) ManagerOption {
	return func(m *Manager) { m.testSource = src }
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

// NewManager constructs the production owner. caller reports the live cloud login
// state (production *graphclient.Router.LoggedIn) so the source factory selects
// the GCS source when logged in and the L2-local source otherwise. cacheDir roots
// the per-graph L2 disk caches; maxBytes <= 0 means an unbounded cache.
//
// opts are optional construction knobs; WithSegmentTransport supplies the cloud
// segment-transport builder that selects the GCS source on the logged-in path.
func NewManager(caller loginState, cacheDir string, maxBytes int64, opts ...ManagerOption) *Manager {
	m := &Manager{
		caller:       caller,
		cacheDir:     cacheDir,
		maxBytes:     maxBytes,
		managers:     make(map[graphKey]*distManager[[]byte, struct{}]),
		bm25Managers: make(map[graphKey]*distManager[bm25.Query, *bm25.CorpusStats]),
		dirty:        make(map[graphKey]*graphDirtyState),
		tombstoned:   make(map[graphKey][]searchengine.ExternalID),
		tombstoneSeq: make(map[graphKey]map[searchengine.ExternalID]uint64),
		rebuildWork:  make(map[graphKey]*stagedRebuild),
	}
	for _, opt := range opts {
		opt(m)
	}
	return m
}
