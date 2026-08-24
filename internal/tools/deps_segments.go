// SPDX-License-Identifier: Apache-2.0

// deps_segments.go — the SEGMENT-ENGINE consumer seams and their report types:
// search, vector-by-id, ship/finalize, prune-cache, delete, coverage, and the
// pipeline scan the rebuild driver pages. Relocated verbatim from deps.go, which
// keeps the client-wide seams (backend resolution, the graph caller, liveness) and
// the ClientDeps aggregate.
//
// They are one family on purpose: every one is a NARROW per-purpose view of the
// same concrete *segmentdist.Manager, kept separate from each other so the many
// Search-only test doubles compile unchanged when a write-side seam grows. Reading
// them together is what makes that deliberate narrowness visible.

package tools

import (
	"context"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// SegmentSearcher is the narrow consumer-side seam the search intercepts use to
// query the client-hosted BM25+HNSW segment engines. *segmentdist.Manager
// satisfies it (Manager.Search). Declared here as an interface — not the
// concrete type — so the search arms reach the engine without tools importing
// segmentdist's full surface, and so tests can inject a fake Manager that
// asserts the arm drove the CLIENT engine instead of dispatching a server
// search. Returns RRF-fused ranked Hits (ID + fused score) for hydration.
type SegmentSearcher interface {
	Search(ctx context.Context, gt kgtypes.GraphType, name, queryText string, queryVec []byte, k int) ([]searchengine.Hit, error)
}

// SegmentOverlaySearcher is the narrow consumer-side seam the BRANCH code search
// uses to query a base graph and its branch-overlay graph as ONE corpus.
// *segmentdist.Manager satisfies it (Manager.SearchOverlay). Kept SEPARATE from
// SegmentSearcher for the same reason SegmentVectorResolver is — folding a second
// method in would break every Search-only test double — and because the two seams
// answer different questions: one pool, or a base plus its overlay.
//
// It exists as its own arm rather than as two SegmentSearcher calls merged by the
// caller because the two pools can only be ranked against each other BEFORE
// fusion, where raw engine scores still carry magnitude. A caller holding two
// already-fused hit lists has nothing comparable left to order them by.
type SegmentOverlaySearcher interface {
	SearchOverlay(ctx context.Context, gt kgtypes.GraphType, base, overlay, queryText string, queryVec []byte, k int) ([]searchengine.Hit, error)
}

// SegmentVectorResolver is the narrow consumer-side seam the mode:"similar" search
// claim uses to resolve a node's STORED query vector from the client-local HNSW
// segments by external id. *segmentdist.Manager satisfies it (Manager.VectorByID).
// Kept DELIBERATELY SEPARATE from SegmentSearcher — not folded into it — so the
// ~15 Search-only test doubles (fakeSegmentSearcher, recallFakeSearcher,
// fanOutSegmentSearcher, every SegmentManager() stub) compile unchanged; a narrow
// per-purpose seam over the same concrete is the established deps.go pattern
// (SegmentShipper, PipelineScanner, ReflectionForcer). The (ok=false, err=nil)
// tuple separates absent-id (node not embedded yet → caller loud-errors) from a
// load failure (err!=nil).
type SegmentVectorResolver interface {
	VectorByID(ctx context.Context, gt kgtypes.GraphType, name, externalID string) ([]byte, bool, error)
}

// SegmentShipper is the stage-then-finalize SHIP surface the rebuild_segments driver
// drives. *segmentdist.Manager satisfies it. The method set is DELIBERATELY the
// STAGE-ONLY + single-finalize shape (there is no ship-per-partition entry point at
// all): the driver builds every partition's Documents concurrently, STAGES them one
// call per bucket — nothing written to an engine, nothing shipped — then finalizes
// exactly ONCE via the single serial FinalizeRebuild after the concurrent pool joins.
// That is the fix for the concurrent-ship/reconcilePrune data-loss race: a single ship
// over the fully-built layer can only prune genuinely superseded ids, never a live
// concurrently-built sibling.
//
// STAGING REPLACED AN INCREMENTAL ADD, and the difference is the defect this arc
// closed. Adding partitions one at a time polluted the SERVING engine before the
// replacement was ready, so the finalize published the union of every layer that engine
// had held. Staging keeps both serving engines untouched until one swap per format
// replaces each layer whole — which also removes the per-group seal the old shape
// needed to stop two buckets merging into one segment.
//
// FinalizeRebuild RETURNS what each format retired; the driver feeds the HNSW set to
// InvalidateLocal so the superseded local .seg files are evicted rather than orphaning
// under an unbounded cache. It ALSO reports whether the finalize actually landed a
// manifest swap, which its error cannot: a publish is skipped with a nil error whenever
// the coverage gate rejects a degenerate live set or the agent reports a blob it has not
// yet seen, and a driver that read the error as the completion signal would treat every
// skip as a success.
//
// The durable per-graph record rides this seam rather than a separate one because
// only this driver reads or writes it, and what it records — how far the last
// LANDED rebuild scanned, and which deleted ids the shipped blobs still carry — is
// produced by exactly the ship sequence above. Keeping it here means the driver
// cannot hold a ship surface without the record that ship advances.
type SegmentShipper interface {
	// StageRebuildPartition stages ONE partition of a reset rebuild — BOTH formats, in
	// one call — for the finalize to build, ship and swap. It adds nothing to any engine
	// and ships nothing.
	//
	// ONE CALL CARRIES BOTH FORMATS, and that is a correctness device. A caller
	// structurally cannot stage the vector share of a partition and forget the field
	// share, which is exactly how the field corpus came to lag the vector corpus. The
	// two shares come from the SAME bucket grouping the driver already computed.
	StageRebuildPartition(ctx context.Context, gt kgtypes.GraphType, name string, hnswDocs, bm25Docs []searchengine.Document) error

	// FinalizeRebuild is the ONE serial finalize of a reset rebuild, called after every
	// partition has been staged. Each format's layer is built ASIDE, shipped, gated,
	// swapped in with one CAS and published — so the corpus is never half-replaced.
	//
	// IT REPORTS RETIREMENT PER FORMAT, and the split has a measured origin: a live run
	// read "0 superseded segments pruned" while all eight bm25 blobs retired, because
	// the finalize returned the HNSW set alone and that format had already converged.
	// The two corpora carry separate manifests and retire independently.
	//
	// Swapped is NOT derivable from the error: both the coverage gate and the agent's
	// 409 decline with a nil error, so a caller reading the error as the completion
	// signal treats every skip as a success.
	FinalizeRebuild(ctx context.Context, gt kgtypes.GraphType, name string) (RebuildFinalizeResult, error)

	// InvalidateLocal evicts superseded segments from the local L2 cache. It is fed the
	// HNSW retired set ONLY, which is correct: BM25 L2 orphans are PruneCache's job and
	// reporting a retired set is not the same as evicting it.
	InvalidateLocal(gt kgtypes.GraphType, name string, ids []searchengine.SegmentID)

	// ReEmitRebuiltDelta is the DELTA finalize: it re-emits the partitions owning the
	// supplied documents against the corpus the serving engines already hold, rather
	// than sealing the scanned window into a segment of its own.
	ReEmitRebuiltDelta(
		ctx context.Context, gt kgtypes.GraphType, name string, hnswDocs, bm25Docs []searchengine.Document,
	) (RebuildDeltaResult, error)

	// PublishedManifestCount reads the manifest BACK FROM THE SOURCE and reports how
	// many entries it holds for one format. The driver uses it on a FULL/RESET rebuild
	// to check the published cardinality against what it reported building.
	//
	// IT IS A SOURCE READ ON PURPOSE. The driver already knows its own build count and
	// could compare it to any number derived from the same in-process data — but that
	// comparison is an identity, and cannot fail for the reason the check exists. Only
	// what the server actually published can disagree with what the driver thinks it
	// built, so only a read-back closes the gap. This is the ONE surface added for it —
	// the finalize's result is deliberately NOT widened to carry a cardinality, because
	// a number the finalize computed is exactly the in-process value this check exists
	// to distrust.
	PublishedManifestCount(ctx context.Context, gt kgtypes.GraphType, name, format string) (int, error)

	// LoadRebuildState reads the durable per-graph record: the server-served horizon
	// the last landed rebuild scanned up to, and the deleted ids the shipped blobs
	// still carry. A graph with no record reports a zero watermark and no ids — the
	// full-corpus scan, which is what a fresh daemon and a wiped cache both want.
	LoadRebuildState(gt kgtypes.GraphType, name string) (watermarkNanos int64, tombstoned []searchengine.ExternalID, err error)
	// SaveRebuildState replaces that record. The watermark and the tombstone set are
	// written in ONE call because they are one durable fact: a watermark advanced
	// past ids the record never learned would mean those ids are never scanned
	// again, and the window they describe would silently reopen.
	SaveRebuildState(gt kgtypes.GraphType, name string, watermarkNanos int64, tombstoned []searchengine.ExternalID) error
	// LoadMergeWatermark reads the OTHER client-side consumer's durable position:
	// the delta-merge horizon. It is here so the retention floor can be taken
	// across BOTH consumers of the erase feed before any scan reports a position
	// to the server — see retentionFloorFor.
	//
	// CLIENT-INTERNAL, NOT WIRE. This widens a Go interface the client owns; the
	// request field the floor is written into already exists on the wire and is
	// unchanged.
	LoadMergeWatermark(gt kgtypes.GraphType, name string) (int64, error)
	// SetGraphTombstones hands the live set to the segment engines, so every Import
	// seeds those ids dead and a blob shipped BEFORE a delete cannot resurrect the
	// removed node. The driver is the producer: it is what reads the delete feed.
	SetGraphTombstones(gt kgtypes.GraphType, name string, ids []searchengine.ExternalID)
	// NoteDeletedIDs is the TIMING half of that pair: SetGraphTombstones says WHAT is
	// dead, this says WHEN it was learned, stamping each id with the write sequence in
	// force right now. A caller that reports a delete must drive BOTH, or the write
	// path cannot tell a genuine re-creation from a stale write that was already queued
	// when the delete landed.
	//
	// The ids passed are the ones the CURRENT window reported, never the accumulated
	// set — re-stamping old deletes would suppress writes that legitimately followed
	// them.
	NoteDeletedIDs(gt kgtypes.GraphType, name string, ids []searchengine.ExternalID)
}

// RebuildFinalizeResult is what one RESET finalize reports back: what each format
// retired, and whether the swap landed for both.
//
// RETIREMENT IS PER FORMAT because the two corpora carry SEPARATE manifests and retire
// independently. A single collapsed count reports one format's silence as the whole
// story — a live run read zero while eight blobs retired on the other format.
//
// Swapped is true only when BOTH formats swapped: a caller told "finalized" after a
// single-format swap would treat an unpublished corpus as durable.
type RebuildFinalizeResult struct {
	HNSWSuperseded []searchengine.SegmentID
	BM25Superseded []searchengine.SegmentID
	Swapped        bool
}

// RebuildDeltaResult is what one delta finalize reports back. The three fields are
// THREE DIFFERENT QUESTIONS and collapsing any two of them is a corpus-wipe bug:
//
//   - Applicable=false means the shape could not be attempted at all — the serving
//     engines are not holding a corpus to re-emit against. The driver must fall back
//     to a from-scratch run, RE-SCANNING from a zero watermark; the delta-scoped items
//     it holds are not a corpus and publishing them as one would reap the rest.
//   - Swapped=false with Applicable=true is a legitimately DEFERRED publish (the
//     coverage gate or an agent 409, both nil-error). It holds the watermark and
//     retries next pass, exactly as the full path does. Reading it as "not applicable"
//     would fire a full re-scan on every deferred publish.
//   - DerivedBucketCount is the partition count the re-emit actually ran against. The
//     driver needs it for the cardinality read-back: a delta that realigns its touched
//     partitions across a power-of-two boundary legitimately GROWS the manifest, so
//     the equality check is only meaningful while that count holds still.
//
// One struct rather than a growing tuple keeps the seam at one method.
type RebuildDeltaResult struct {
	Swapped            bool
	Applicable         bool
	DerivedBucketCount int
}

// SegmentPruner is the narrow seam the one-shot manage(prune-cache) handler drives
// to reclaim orphaned L2 segment files. *segmentdist.Manager satisfies it (via the
// bootstrap client_segment.go adapter — the ONLY place the tools-local and
// segmentdist-native vocabularies meet). The targets cross this seam as PARALLEL
// slices (graphTypes[i] pairs with names[i]) of already-imported kgtypes.GraphType
// + string — DELIBERATELY not a segmentdist target type — so tools never imports
// segmentdist (the same intra-client decoupling the four sibling segment seams keep:
// this file references *segmentdist.Manager in PROSE only, never in a signature or a
// var _ assertion). execute=false previews (the report carries the would-remove
// orphans, deletes nothing); execute=true unlinks the orphans and fills
// Removed/RemovedBytes.
type SegmentPruner interface {
	PruneCache(ctx context.Context, graphTypes []kgtypes.GraphType, names []string, execute bool) (PruneCacheReport, error)
}

// SegmentDeleter is the narrow seam the mutate(delete) and manage(prune) handlers
// drive so a removed node leaves this client's SHIPPED segment corpus, not just
// its in-memory live set. *segmentdist.Manager satisfies it (DeleteFromBuckets),
// supplied through the client_segment.go adapter — the ids cross as already-imported
// searchengine.ExternalID values, so tools never imports segmentdist, exactly like
// the sibling seams above.
//
// BEST-EFFORT BY CONTRACT: a removal must never be reported as failed because the
// re-emit failed, so callers log and swallow. RECOVERY IS ASYMMETRIC between the
// two callers. On the mutate(delete) path the row is only tombstoned, so a dropped
// re-emit self-heals the next time anything touches the partition. On the
// manage(prune) path the rows are HARD-deleted server-side and no later scan can
// re-learn them, so a dropped re-emit leaves those documents in the shipped corpus
// until a full rebuild.
type SegmentDeleter interface {
	DeleteFromBuckets(ctx context.Context, gt kgtypes.GraphType, name string, ids []searchengine.ExternalID) error
}

// PruneCacheGraphReport is the tools-local per-(graph, format) prune result — a
// field-identical mirror of segmentdist.PruneCacheGraphReport over already-imported
// types only (kgtypes.GraphType + searchengine.SegmentID). The client_segment.go
// adapter copies it field-for-field across the package boundary. Orphans is the
// would-remove (preview) OR did-remove set; Bytes is the summed .seg FileInfo size;
// Aborted+AbortReason surface a List(0) subset-abort for a SKIPPED pool.
type PruneCacheGraphReport struct {
	GraphType   kgtypes.GraphType
	Name        string
	Format      string
	Orphans     []searchengine.SegmentID
	Bytes       int64
	Aborted     bool
	AbortReason string
}

// PruneCacheReport is the tools-local whole-run result mirroring
// segmentdist.PruneCacheReport: one PruneCacheGraphReport per (graph, format) pool
// plus the EXECUTED totals (Removed count + RemovedBytes), zero on a preview run.
type PruneCacheReport struct {
	Graphs       []PruneCacheGraphReport
	Removed      int
	RemovedBytes int64
}

// SegmentCoverageReader is the narrow read seam the manage(status) segment-coverage
// column uses to read a graph's segment-covered doc count (summed HNSW
// meta.DocCount). *segmentdist.Manager satisfies it (Manager.ShippedSegmentDocCount).
// A narrow per-purpose seam over the same concrete is the established deps.go
// pattern (SegmentSearcher, SegmentShipper, SegmentVectorResolver). The renderer
// consumes only the covered count; anyUnknown (the conservative-unknown signal the
// auto-heal probe reads) is irrelevant to a display column and ignored there.
type SegmentCoverageReader interface {
	ShippedSegmentDocCount(ctx context.Context, gt kgtypes.GraphType, name string) (covered int, anyUnknown bool, err error)
	// ResidentDocCount returns the LIVE in-memory engine resident doc count for one
	// graph — the searchable pool's actual size, distinct from the SERVER's shipped
	// count above. The status column renders both so a collapse (server intact, live
	// pool empty) shows "live 0 of N" instead of being masked behind the shipped
	// figure. Satisfied by *segmentdist.Manager.ResidentDocCount (a single atomic
	// read, no RPC).
	ResidentDocCount(gt kgtypes.GraphType, name string) int
	// LiveResidentDocCount returns the DISTINCT LIVE-SEARCHABLE doc count — what a
	// search could actually return, counted once each. It is what the status column
	// renders as live_resident; ResidentDocCount above stays the operand the heal
	// and publish gate compare against, so the reporter and the decider read the
	// same predicate without sharing a number. Like its sibling it does NOT load,
	// which keeps the column's local-read contract intact.
	LiveResidentDocCount(gt kgtypes.GraphType, name string) int
	// RepairVerification returns the graph's persisted coverage-backstop record as
	// this process last saw it, and ok=false when this process has not loaded that
	// graph's record at all. It is a PURE MAP READ on the other side of the seam — no
	// disk, no RPC — which is what keeps it off the serial assembly walk this file's
	// collector documents itself as keeping cheap; ok=false is rendered as the honest
	// "not verified this process" answer rather than as a degradation.
	RepairVerification(gt kgtypes.GraphType, name string) (RepairVerification, bool)
	// LoadRebuildState / LoadMergeWatermark expose THIS CLIENT'S OWN consumer
	// positions so the status row can render how long since each last advanced.
	//
	// THEY ANSWER THE HALF THE SERVER CANNOT. The server's backlog counters say how
	// much is piling up; only the client knows whether ITS consumers are still
	// moving — and a consumer that stops arriving is exactly the case the
	// arrival-driven stall alarm structurally cannot report. Both are local reads,
	// no RPC and no proto, which keeps the column's local-read contract intact.
	LoadRebuildState(gt kgtypes.GraphType, name string) (watermarkNanos int64, tombstoned []searchengine.ExternalID, err error)
	LoadMergeWatermark(gt kgtypes.GraphType, name string) (int64, error)
}

// RepairVerification is the coverage column's view of the backstop's per-graph
// record. It is declared HERE, as a narrow carrier, rather than shared with the
// segment package that owns the record: that package's own in-package tests import
// this one, so a production import in this direction is an import cycle. The
// composition root adapts between the two, which is where the two layers already
// meet.
//
// THE THREE FIELDS ANSWER DIFFERENT QUESTIONS AND THE COLUMN NEEDS ALL THREE.
// Converged says the backstop has nothing to do for this graph. Scanned says
// something actually EXAMINED it — the backstop writes a record on two paths, and the
// one that merely DECLINED a graph leaves Scanned false. VerifiedAtNanos says when,
// so a record older than the backstop interval stops being trusted.
type RepairVerification struct {
	Residue         int
	Converged       bool
	Scanned         bool
	VerifiedAtNanos int64
}

// PipelineScanner is the login-routed PipelineScan + Execute wire seam the
// rebuild_segments driver pages the segment_rebuild scan through. GraphCaller
// exposes only Execute and the *graphclient.Router has NO PipelineScan — only the
// bootstrap routedWireClient does — so this is a distinct accessor satisfied by a
// login-routed adapter (per-call cloud-when-logged-in / local-otherwise).
type PipelineScanner interface {
	PipelineScan(ctx context.Context, req *knowledgev1.PipelineScanRequest) (*knowledgev1.PipelineScanResponse, error)
	Execute(ctx context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error)
}
