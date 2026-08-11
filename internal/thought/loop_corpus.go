// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"context"
	"log/slog"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// loop_corpus.go wires the resident thought-corpus cache into the
// propagation pass: a per-(non-quiet)-tick delta drain that keeps the cache fresh
// with O(changes) reads instead of an hourly full re-drain. Placed behind the
// UNCHANGED quiet-tick gate (runPass returns before refreshCorpusCache on
// a quiet tick, so a quiet tick issues ZERO CorpusDelta calls).
//
// T4b — CRASH-MID-DRAIN SEMANTICS: the corpusCache + its per-layer cursors are
// RESIDENT-ONLY (in-memory, never persisted). A daemon crash/restart mid-drain
// simply drops the partial cache → the next start is a COLD FULL DRAIN (empty
// cursors → the delta drain returns the whole corpus). A pinned horizon lives only
// for the duration of one in-process drain loop and dies with the process, so a
// crash can NEVER resume a half-applied delta against a stale pinned H.

// CorpusDeltaScanner is the package-local CorpusDelta seam the resident
// thought-corpus cache pages its per-tick delta drain through — a twin of
// PipelineScanner (wire.go). Kept package-local (the wire contract is the
// generated proto, not a shared Go type); the bootstrap routedWireClient
// satisfies it. A nil scanner leaves the loop in degraded mode (the full
// drainThoughtBrowse path, behavior-equivalent to pre-cache).
type CorpusDeltaScanner interface {
	CorpusDelta(ctx context.Context, req *knowledgev1.CorpusDeltaRequest) (*knowledgev1.CorpusDeltaResponse, error)
}

// corpusNodeTypes is the LOCKED thought-corpus node-type set the resident cache
// drains and the propagation pass reflects over. The three types are the whole
// reasoning corpus; NodeDocument (topic labels) is DELIBERATELY excluded — the
// topic-doc drains keep their own drainThoughtBrowse path (T2-C).
var corpusNodeTypes = []string{
	string(kgtypes.NodeThought),
	string(kgtypes.NodeCharge),
	string(kgtypes.NodeThoughtSession),
}

// corpusDeltaPageSize is the per-page cap for the delta drain. A dirty tick's
// change set is tiny, so one short page terminates it; a burst drains in
// ceil(M/pageSize) pages, all anchored to page 1's pinned horizon.
const corpusDeltaPageSize = 500

// CorpusSource is the optional resident thought-corpus seam a full-corpus consumer
// reads its NodeThought set from instead of re-draining the wire.
// *PropagationLoop implements it (CorpusSnapshot). A nil CorpusSource — a degraded
// loop with no cache/scanner, an on-demand handler with the reflection loop not
// running in-process, or a unit test — makes the consumer fall back to the
// drainThoughtBrowse path, behavior-equivalent to the pre-cache full re-drain.
type CorpusSource interface {
	// CorpusSnapshot returns the resident live NodeThought set and warm=true once the
	// daemon corpus cache has been cold-filled this process; warm=false ⟹ the caller
	// drains the wire (cold start / just-Reset resync / degraded loop).
	CorpusSnapshot() ([]*knowledgev1.Node, bool)
}

// ChargeCorpusSource is the NodeCharge projection of the same resident cache, kept
// as its own interface so a consumer that wants charges rather than thoughts
// type-asserts for it off a CorpusSource (the idiom loop.go uses for reflectProbe).
// *PropagationLoop implements it. The tension universe is its one consumer: charges
// are the seed set from which the charged claim nodes are derived.
type ChargeCorpusSource interface {
	// ChargeSnapshot returns the resident live NodeCharge set, warm=true, under the
	// same cold/Reset/degraded semantics as CorpusSnapshot.
	ChargeSnapshot() ([]*knowledgev1.Node, bool)
}

// CorpusSnapshot returns the resident live NodeThought set from the daemon corpus
// cache and a warm flag, satisfying CorpusSource. warm is false for a nil loop, an
// unwired cache, or an EMPTY cache (cold start before the first refreshCorpusCache,
// or immediately after a forced-resync Reset) — in every such case the caller drains
// the wire rather than reflecting over an empty snapshot. Guarded by the cache's own
// mutex (Snapshot), mirroring the p.mu-guarded GetClustersCached cold-sentinel
// accessor. Filters to NodeThought because the cache co-resides charges + sessions
// (corpusNodeTypes) but the rewired consumers want only the thought node set; charge
// hydration keeps its own EdgeChargedBy walk (fetchChargesFor), unchanged.
//
// The loop refreshes the cache at the TOP of runPass (before detection/propagation),
// so a warm snapshot read by a rewired consumer this tick reflects the corpus as of
// this tick's pinned safe horizon.
func (p *PropagationLoop) CorpusSnapshot() ([]*knowledgev1.Node, bool) {
	if p == nil || p.corpus == nil {
		return nil, false
	}
	all := p.corpus.Snapshot()
	if len(all) == 0 {
		return nil, false // cold / just-Reset — not warm; caller drains.
	}
	thoughts := make([]*knowledgev1.Node, 0, len(all))
	for _, n := range all {
		if kgtypes.NodeType(n.GetType()) == kgtypes.NodeThought {
			thoughts = append(thoughts, n)
		}
	}
	return thoughts, true
}

// ChargeSnapshot returns the resident live NodeCharge set from the same daemon
// corpus cache, satisfying ChargeCorpusSource. An exact twin of CorpusSnapshot with
// the type filter moved to NodeCharge: the cache already co-resides charges
// (corpusNodeTypes), so this is a projection of resident data and issues ZERO wire
// calls. warm is false for a nil loop, an unwired cache or an EMPTY cache — in every
// such case the caller drains a type=charge browse instead.
func (p *PropagationLoop) ChargeSnapshot() ([]*knowledgev1.Node, bool) {
	if p == nil || p.corpus == nil {
		return nil, false
	}
	all := p.corpus.Snapshot()
	if len(all) == 0 {
		return nil, false // cold / just-Reset — not warm; caller drains.
	}
	charges := make([]*knowledgev1.Node, 0, len(all))
	for _, n := range all {
		if kgtypes.NodeType(n.GetType()) == kgtypes.NodeCharge {
			charges = append(charges, n)
		}
	}
	return charges, true
}

// thoughtCorpus is the single funnel every rewired thought-node consumer takes: a
// warm CorpusSource serves the resident NodeThought snapshot (O(1) resident read),
// while a nil/cold source drains the full thought browse (drainThoughtBrowse) — the
// pre-cache behavior every existing unit test and the cold/resync/degraded path
// keep. Replaces the bare fetchAllThoughtNodes drain at the rewired call sites.
func thoughtCorpus(ctx context.Context, gc Caller, src CorpusSource) ([]*knowledgev1.Node, error) {
	if src != nil {
		if nodes, warm := src.CorpusSnapshot(); warm {
			return nodes, nil
		}
	}
	return drainThoughtBrowse(ctx, gc, string(kgtypes.NodeThought), browsePageSize)
}

// refreshCorpusCache brings the resident cache up to the current safe horizon on a
// non-quiet tick: drain the delta (cold = empty cursors → full corpus; warm =
// O(changes)), reconcile against the final page's probe at the pinned H, and force
// a full resync on a genuine divergence. Nil-tolerant: a loop with no cache/scanner
// (degraded / test fake) is a no-op, leaving consumers on the full-drain path.
func (p *PropagationLoop) refreshCorpusCache(ctx context.Context) {
	if p == nil || p.corpus == nil || p.corpusScanner == nil {
		return
	}
	cold := len(p.corpus.Snapshot()) == 0
	final, pages, items, err := p.drainCorpusDelta(ctx)
	if err != nil {
		slog.Warn("thought: corpus delta drain failed — cache not refreshed this tick", "err", err)
		return
	}
	// Reconcile at the pinned H against the final page's per-layer probes. A
	// mismatch is the PRODUCTION-RED cache-correctness check: reset + re-drain.
	if !p.corpus.Reconcile(final) {
		slog.Warn("thought: corpus cache reconciliation mismatch — forced full resync",
			"safe_horizon", final.GetSafeHorizon(), "delta_items", items, "pages", pages)
		p.corpus.Reset()
		if _, _, _, err := p.drainCorpusDelta(ctx); err != nil {
			slog.Warn("thought: corpus cache forced resync drain failed", "err", err)
		}
		return
	}
	// Staleness bound: horizon age = now − pinned H. It measures FRESHNESS OF THE
	// MERGED SNAPSHOT, not the liveness of any publisher — the horizon is computed
	// by the very request that returns it, so there is nothing standing whose stall
	// this number could reveal.
	//
	// Read it against the drain shape, which the `cold` and `pages` fields below
	// disclose. H is PINNED at page 1 and reused for every later page (see
	// drainCorpusDelta), and the age is measured against that pinned H at merge
	// time. So a warm SINGLE-PAGE tick reads about epsilon plus one round trip,
	// while a cold MULTI-PAGE drain reads epsilon plus the drain's own wall-clock
	// duration. A large value on a multi-page cold drain is expected, not a fault.
	horizonAgeMs := (time.Now().UnixNano() - final.GetSafeHorizon()) / int64(time.Millisecond)
	slog.Info("thought: corpus delta merged",
		"horizon_age_ms", horizonAgeMs, "delta_items", items, "pages", pages, "cold", cold)
}

// drainCorpusDelta pages CorpusDelta from the cache's current cursors to the
// horizon, MERGING each page into the cache. It PINS page 1's safe horizon across
// every subsequent page (T2-3): page 1 sends pinned_horizon=0 and captures the
// server's fresh H; every later page sends that H (with the advancing cursors), so
// the whole drain — scan and the final-page reconciliation probe — is anchored to
// ONE horizon. Without pinning, page 2 would recompute a fresh, non-monotonic H
// that could regress below page 1's cursor → an empty page + stranded rows + a
// spurious resync. Returns the FINAL page (for reconciliation), the page count, and
// the total item count. Termination: a short/empty page (< pageSize).
func (p *PropagationLoop) drainCorpusDelta(ctx context.Context) (final *knowledgev1.CorpusDeltaResponse, pages, items int, err error) {
	// Background loop with no originating tool call — it stamps its own
	// query-origin operation so the corpus drain's cost is attributable.
	ctx = graphclient.WithOperation(ctx, graphclient.OpCorpusDeltaDrain)
	pinnedHorizon := int64(0)
	for {
		resp, rerr := p.corpusScanner.CorpusDelta(ctx, &knowledgev1.CorpusDeltaRequest{
			GraphType:     string(kgtypes.GraphKnowledge),
			GraphName:     "default",
			NodeTypes:     corpusNodeTypes,
			Cursors:       p.corpus.Cursors(),
			Limit:         corpusDeltaPageSize,
			PinnedHorizon: pinnedHorizon,
		})
		if rerr != nil {
			return nil, pages, items, rerr
		}
		pages++
		items += len(resp.GetItems())
		p.corpus.MergeDelta(resp)
		if pinnedHorizon == 0 {
			pinnedHorizon = resp.GetSafeHorizon() // pin page 1's H for the rest of the drain.
		}
		final = resp
		if len(resp.GetItems()) < corpusDeltaPageSize {
			break // short/empty final page — drain exhausted.
		}
	}
	return final, pages, items, nil
}
