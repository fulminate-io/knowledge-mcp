// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// ShipManager is the narrow surface the embed-writeback seam uses to build + ship
// client-side segments. *segmentdist.Manager satisfies it (dual-format after
// AddAndShip feeds the HNSW engine from vectors; AddAndShipFields feeds
// the BM25 engine from per-field text). Declared here (consumer-side interface) so
// the pipeline carries no hard dependency on the segmentdist concrete type and
// tests can inject a fake.
type ShipManager interface {
	AddAndShip(ctx context.Context, gt kgtypes.GraphType, name string, docs []searchengine.Document) error
	// AddAndShipFields builds + ships BM25 segments from field-bearing Documents.
	// Best-effort at the call site — a failure WARNs and never fails embed
	// writeback; the BM25 segments self-heal on the next ship (server-side search
	// is retired, so these segments are the only BM25 index).
	AddAndShipFields(ctx context.Context, gt kgtypes.GraphType, name string, docs []searchengine.Document) error
	// Flush force-seals the sub-threshold coalescing tail of BOTH formats for one
	// (gt, name) and ships the newly-sealed segments. The quiescence trigger
	// fires it once per embed drain so sub-1024-doc graphs + trailing
	// tails seal and become client-searchable. *segmentdist.Manager.Flush
	// (manager_owner.go:110) satisfies it.
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
