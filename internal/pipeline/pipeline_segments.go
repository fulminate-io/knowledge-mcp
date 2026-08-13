// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"

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
	// marks them for re-emit. Best-effort at the call site — a failure WARNs and
	// never fails embed writeback; the BM25 segments self-heal on the next write
	// (server-side search is retired, so these segments are the only BM25 index).
	AddAndMarkDirtyFields(ctx context.Context, gt kgtypes.GraphType, name string, docs []searchengine.Document) error
	// Flush force-seals the sub-threshold coalescing tail of BOTH formats for one
	// (gt, name) and ships the newly-sealed segments. The quiescence trigger
	// fires it once per embed drain. Since the writes above already force-seal,
	// Flush finds an empty buffer on THIS path and is a no-op here; its contract
	// stays live for the migration one-shot and for direct-engine construction.
	// *segmentdist.Manager.Flush satisfies it (cited by method, not line — this
	// ticket's own deletions move it).
	Flush(ctx context.Context, gt kgtypes.GraphType, name string) error
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

// AttachCollectGateFactory wires the per-graph collect-gate predicate factory.
// Called once at construction (bootstrap) before Start; nil-safe — leaving it
// unset means RegisterGraph builds a nil per-collector predicate and the scan loop
// gates on nothing, which is the pre-gate behavior. The factory is built in
// bootstrap (over the collect runtime) so this package never imports the tools
// package that owns it.
func (p *Pipeline) AttachCollectGateFactory(fn func(kgtypes.GraphType, string) func() bool) {
	p.collectGateFactory = fn
}
