// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
	"github.com/fulminate-io/knowledge-mcp/internal/workingset"
)

// ShipManager is the narrow surface the embed-writeback seam uses to build + ship
// client-side segments. *segmentdist.Manager satisfies it (dual-format after
// AddAndMarkDirty feeds the HNSW engine from vectors; AddAndMarkDirtyFields feeds
// the BM25 engine from per-field text). Declared here (consumer-side interface) so
// the pipeline carries no hard dependency on the segmentdist concrete type and
// tests can inject a fake.
//
// NEITHER WRITE SHIPS. Each makes its documents searchable in this process at once
// and records the partitions they touched; the owner re-emits and publishes those
// partitions on its own reconcile tick, which the pipeline never calls. So a ship
// or publish failure is NOT observable at this seam — only an add or seal failure
// is, and both writes are best-effort against that.
type ShipManager interface {
	AddAndMarkDirty(ctx context.Context, gt kgtypes.GraphType, name string, docs []searchengine.Document) error
	// AddAndMarkDirtyFields seals BM25 segments from field-bearing Documents and
	// marks them for re-emit. Its caller is the BM25 arm (collector_bm25.go), which
	// drains the CorpusDelta feed — NOT the embed writeback, which stopped shipping
	// BM25 when the two axes were decoupled. Best-effort at the call site: a failure
	// WARNs and never fails the arm's drain, and the arm does not advance its cursor
	// past a failed page, so the BM25 segments self-heal by re-riding the feed
	// (server-side search is retired, so these segments are the only BM25 index).
	AddAndMarkDirtyFields(ctx context.Context, gt kgtypes.GraphType, name string, docs []searchengine.Document) error
	// Flush force-seals the sub-threshold coalescing tail of BOTH formats for one
	// (gt, name) and ships the newly-sealed segments. TWO quiescence triggers fire
	// it now, once per drain each: the embed axis's and the BM25 arm's
	// (maybeBM25Flush) — a fully-embedded graph has no embed work to carry the edge,
	// so the arm cannot rely on the embed one. Each leg of the dual-format flush is
	// gated on its own hasUnwrittenExport(), so the second caller on a quiet format
	// is a true no-op rather than a competing sealer. Since the writes above already
	// force-seal,
	// Flush finds an empty buffer on THIS path and is a no-op here; its contract
	// stays live for the migration one-shot and for direct-engine construction.
	// *segmentdist.Manager.Flush satisfies it (cited by method, not line — this
	// ticket's own deletions move it).
	Flush(ctx context.Context, gt kgtypes.GraphType, name string) error
	// DeleteFromBuckets removes ids from their routed buckets. The BM25 arm hands
	// it the tombstoned rows its delta window served — which is what makes a delete
	// reach the durable blob rather than only the in-memory live set. Unlike the two
	// writes above it is NOT best-effort at the call site: a dropped delete leaves a
	// removed node searchable forever, so the arm holds its cursor and retries.
	DeleteFromBuckets(ctx context.Context, gt kgtypes.GraphType, name string, ids []searchengine.ExternalID) error
	// LoadBM25Cursors / SaveBM25Cursors are the BM25 arm's durable per-graph
	// position on the CorpusDelta feed. They are on THIS interface rather than
	// reached through a second seam because the arm's closures are built from the
	// same p.segmentMgr the flush closure is, and a second optional interface for
	// two methods would be a second thing to wire and forget.
	LoadBM25Cursors(gt kgtypes.GraphType, name string) ([]*knowledgev1.LayerCursor, error)
	SaveBM25Cursors(gt kgtypes.GraphType, name string, cursors []*knowledgev1.LayerCursor) error
}

// AttachSegmentManager wires the optional HNSW segment owner. Called once at
// construction (bootstrap) before Start; nil-safe — leaving it unset disables the
// additive client-side build+ship.
func (p *Pipeline) AttachSegmentManager(m ShipManager) { p.segmentMgr = m }

// AttachHealFactory wires the auto-heal closure factory. Called once at
// construction (bootstrap) before Start, after AttachSegmentManager; nil-safe —
// leaving it unset means RegisterGraph builds a nil per-collector heal closure
// and the armed embed-drain heal-check no-ops. The factory is built in bootstrap
// (over the concrete segment probe + tools.RebuildSegments) so this package never
// imports tools.
func (p *Pipeline) AttachHealFactory(fn func(kgtypes.GraphType, string) func(context.Context) error) {
	p.healFactory = fn
}

// AttachBalanceFactory wires the QUIESCENCE-EDGE balance-verdict closure factory.
// Called once at construction (bootstrap) before Start, after AttachSegmentManager;
// nil-safe — leaving it unset means RegisterGraph builds a nil per-collector closure and
// the cross-axis balance edge no-ops.
//
// IT IS SEPARATE FROM AttachHealFactory rather than folded into it, because the two fire
// on DIFFERENT EDGES and answer different questions. The heal closure runs on ONE axis's
// drain and asks whether the pool is lost; this runs only once BOTH axes are drained at
// the current collect epoch and asks the exact balance question. Sharing one hook would
// force the stricter condition onto the heal, which must stay able to rescue a lost pool
// while the other axis is still working.
func (p *Pipeline) AttachBalanceFactory(fn func(kgtypes.GraphType, string) func(context.Context) error) {
	p.balanceFactory = fn
}

// AttachWorkingSet wires the client's interaction-earned working set, which is
// where every catalog pass now gets its wanted set. Called once at construction
// (bootstrap) before Start.
//
// NIL MEANS EMPTY, NEVER UNRESTRICTED. Leaving it unset registers no collectors
// at all rather than falling back to draining everything the account holds, so a
// missed wiring UNDER-admits — visible as a pipeline that enriches nothing —
// instead of silently restoring the account-wide behavior this gate exists to
// remove. Default-deny is the whole point, so the nil case must never grow a
// permissive fallback.
func (p *Pipeline) AttachWorkingSet(ws *workingset.Set) { p.workingSet = ws }

// AttachLocalPresence wires the machine-local presence predicate: given an
// already-admitted graph, may THIS machine do background work on it. Built in
// bootstrap because the answer comes from the repo manifest owned by the tools
// package, which this package must never import (tools already imports pipeline,
// so the reverse edge would be a cycle).
//
// NIL IS PERMISSIVE, the OPPOSITE direction to AttachWorkingSet, and the
// asymmetry is deliberate. The working set defaults to deny because it IS the
// membership decision. Presence only NARROWS a set membership has already
// chosen, so an unwired predicate must leave behavior exactly as it was —
// otherwise every fixture that does not wire one would silently drain nothing
// and the suite would go green on a pipeline that enriches no graph at all.
func (p *Pipeline) AttachLocalPresence(fn func(gt kgtypes.GraphType, name string) bool) {
	p.localPresence = fn
}

// AttachCollectGateFactory wires the per-graph collect-gate predicate factory.
// Called once at construction (bootstrap) before Start; nil-safe — leaving it
// unset means RegisterGraph builds a nil per-collector predicate and the scan loop
// gates on nothing, which is the pre-gate behavior. The factory is built in
// bootstrap (over the collect runtime) so this package never imports the tools
// package that owns it.
func (p *Pipeline) AttachCollectGateFactory(fn func(kgtypes.GraphType, string) func() bool) {
	p.collectGateFactory = fn
}

// AttachCollectEpochFactory wires the per-graph COLLECT EPOCH source — a monotonic
// count of collects into that graph that have ended. A cross-axis quiescence
// observation is stamped with it so the observation expires when a collect lands,
// instead of being read as still valid over rows the collect has just added.
//
// UNSET MEANS NO SOURCE, AND A CONSUMER MUST DECLINE TO EVALUATE — it must NOT
// read as epoch zero. This is the OPPOSITE of AttachCollectGateFactory's nil case,
// and the asymmetry is deliberate rather than an inconsistency. An absent gate
// leaves the scan ungated, which is exactly the pre-gate behaviour and costs
// nothing. An absent epoch read as zero would leave every stamp permanently
// matching, so a quiescence observation could never go stale — and the client with
// no collect runtime is precisely the router-less deployment with nobody watching.
// Refusing to answer is correct there; answering "agreed" is not.
func (p *Pipeline) AttachCollectEpochFactory(fn func(kgtypes.GraphType, string) func() uint64) {
	p.collectEpochFactory = fn
}
