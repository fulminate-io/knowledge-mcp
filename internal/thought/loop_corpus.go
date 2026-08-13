// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"context"
	"log/slog"
	"os"
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
// CRASH-MID-DRAIN SEMANTICS. The corpusCache + its per-layer cursors are PERSISTED
// (warm start), but only from a state that has just reconciled: the record is
// written on the success arm of refreshCorpusCache and removed on the mismatch arm,
// so a partial or divergent cache is never what lands on disk. A crash mid-drain
// therefore leaves either no record (first run) or the last RECONCILED one, and the
// next start resumes from that record's cursors rather than from zero.
//
// A pinned horizon still lives only for the duration of one in-process drain loop
// and dies with the process, so a crash can NEVER resume a half-applied delta
// against a stale pinned H: the adopted record carries cursors, not a horizon, and
// the adopting tick pins a fresh one of its own.
//
// The adopted rows are held UNVALIDATED (corpusUnvalidated) until that tick's
// Reconcile passes, so no consumer can ever observe bytes that came off disk and
// were checked against nothing. If the adopting tick's drain fails, the adopted
// cache is discarded and the process is back to exactly the cold behavior above.

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

// SessionCorpusSource is the NodeThoughtSession projection of the same resident
// cache, kept as its own interface for the same reason ChargeCorpusSource is: a
// consumer that wants session nodes rather than thoughts type-asserts for it off a
// CorpusSource. *PropagationLoop implements it. Its one consumer is the
// session-label hydrate (FetchSessionLabelsByThought), which resolves each
// enclosing session node's display label.
type SessionCorpusSource interface {
	// SessionSnapshot returns the resident live NodeThoughtSession set, warm=true,
	// under the same cold/Reset/degraded semantics as CorpusSnapshot.
	SessionSnapshot() ([]*knowledgev1.Node, bool)
}

// warmLoadCorpusOnce merges the persisted warm-start record into the resident cache,
// ONCE per process, and reports whether it adopted anything.
//
// The rows it merges are UNVALIDATED: they came off disk and have been checked
// against nothing but their own checksum. corpusUnvalidated is set BEFORE the merge
// and the three snapshot accessors report cold while it is set, so the adopted rows
// are invisible to every consumer until the SAME tick's drain reconciles them at a
// pinned horizon — or until they are discarded.
//
// It runs from refreshCorpusCache rather than from Start for the reason
// restoreWatermarkOnce gives for its own deferral: refreshCorpusCache is reached
// only from the admission-gated tick path and the ungated user lever, so a process
// that has never been admitted to the knowledge graph reads no record at all.
func (p *PropagationLoop) warmLoadCorpusOnce() (adopted bool) {
	if p == nil || p.corpusCachePath == "" {
		return false // persistence disabled — resident-only, cold on every restart.
	}
	p.mu.Lock()
	if p.corpusWarmLoadDone {
		p.mu.Unlock()
		return false
	}
	p.corpusWarmLoadDone = true
	p.mu.Unlock()

	// A record must never be merged over a live cache: the resident rows were
	// reconciled at a horizon the record knows nothing about.
	if p.corpus == nil || p.corpus.Len() != 0 {
		return false
	}

	rec, ok, err := loadCorpusRecord(p.corpusCachePath, corpusNodeTypes)
	switch {
	case err != nil:
		// LOUD: a record that exists and does not decode is a fault, not a state.
		slog.Warn("thought: persisted corpus cache rejected — falling back to a full cold drain",
			"err", err, "path", p.corpusCachePath)
		return false
	case !ok:
		// QUIET: absence is the ordinary first-run / wiped-cache state.
		slog.Debug("thought: no persisted corpus cache — cold drain", "path", p.corpusCachePath)
		return false
	}

	// SET BEFORE THE MERGE. Between this Store and the Reconcile that clears it, the
	// rows are resident but unproven, and the readers are concurrent on-demand
	// reflect handlers that take no reflection guard.
	p.corpusUnvalidated.Store(true)
	p.corpus.MergeDelta(rec)
	slog.Info("thought: adopted the persisted corpus cache — held unvalidated until reconciliation",
		"items", len(rec.GetItems()), "cursors", len(rec.GetNextCursors()))
	return true
}

// CorpusSnapshot returns the resident live NodeThought set from the daemon corpus
// cache and a warm flag, satisfying CorpusSource. warm is false for a nil loop, an
// unwired cache, an EMPTY cache (cold start before the first refreshCorpusCache,
// or immediately after a forced-resync Reset), or a cache holding rows ADOPTED FROM
// THE PERSISTED RECORD that this process has not yet reconciled — in every such case
// the caller drains the wire rather than reflecting over an empty or unproven
// snapshot. Guarded by the cache's own
// mutex (Snapshot), mirroring the p.mu-guarded GetClustersCached cold-sentinel
// accessor. Filters to NodeThought because the cache co-resides charges + sessions
// (corpusNodeTypes) but the rewired consumers want only the thought node set; charge
// hydration keeps its own EdgeChargedBy walk (fetchChargesFor), unchanged.
//
// The loop refreshes the cache at the TOP of runPass (before detection/propagation),
// so a warm snapshot read by a rewired consumer this tick reflects the corpus as of
// this tick's pinned safe horizon.
//
// WHY THE UNVALIDATED FLAG IS CHECKED TWICE — here and in both twins below. A single
// pre-check is check-then-read and does not hold: a reader that Loads false an
// instant before warmLoadCorpusOnce's Store(true) can then queue on the cache's own
// mutex behind the adopting MergeDelta and be handed the merged rows anyway. The
// window is sub-microsecond and opens once per process, but "these rows can never be
// served" is a claim, and the post-check is what makes it literally true rather than
// a description of the happy path. The pre-check stays because it is the cheap common
// path — it skips the map copy entirely when the flag is set.
//
// The post-check is sound in both directions only because of the CLEARING RULE: the
// only path that clears the flag is a Reconcile that returned true, so a flag reading
// false after the rows are in hand means those rows are validated. The two discard
// paths (a drain error on the adopting tick, and a Reconcile mismatch) Reset the
// cache and deliberately LEAVE THE FLAG SET, so a reader holding a pre-Reset snapshot
// cannot re-check, see false, and serve rows that were just thrown away.
func (p *PropagationLoop) CorpusSnapshot() ([]*knowledgev1.Node, bool) {
	if p == nil || p.corpus == nil {
		return nil, false
	}
	if p.corpusUnvalidated.Load() {
		return nil, false // adopted from disk, not yet validated — caller drains.
	}
	all := p.corpus.Snapshot()
	if len(all) == 0 {
		return nil, false // cold / just-Reset — not warm; caller drains.
	}
	if p.corpusUnvalidated.Load() {
		return nil, false // an adopt landed mid-read — the rows in hand are unvalidated.
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
// calls. warm is false for a nil loop, an unwired cache, an EMPTY cache, or a cache
// holding rows adopted from the persisted record and not yet reconciled — in every
// such case the caller drains a type=charge browse instead.
//
// GATED LIKE ITS TWIN, NOT INSTEAD OF IT: the three accessors are projections of the
// SAME map, so gating only CorpusSnapshot would leave this one serving the very bytes
// the flag exists to withhold.
func (p *PropagationLoop) ChargeSnapshot() ([]*knowledgev1.Node, bool) {
	if p == nil || p.corpus == nil {
		return nil, false
	}
	if p.corpusUnvalidated.Load() {
		return nil, false // adopted from disk, not yet validated — caller drains.
	}
	all := p.corpus.Snapshot()
	if len(all) == 0 {
		return nil, false // cold / just-Reset — not warm; caller drains.
	}
	if p.corpusUnvalidated.Load() {
		return nil, false // an adopt landed mid-read — the rows in hand are unvalidated.
	}
	charges := make([]*knowledgev1.Node, 0, len(all))
	for _, n := range all {
		if kgtypes.NodeType(n.GetType()) == kgtypes.NodeCharge {
			charges = append(charges, n)
		}
	}
	return charges, true
}

// SessionSnapshot returns the resident live NodeThoughtSession set from the same
// daemon corpus cache, satisfying SessionCorpusSource. An exact twin of
// ChargeSnapshot with the type filter moved to NodeThoughtSession: the cache already
// co-resides sessions (corpusNodeTypes), so this is a projection of resident data and
// issues ZERO wire calls. warm is false for a nil loop, an unwired cache, an EMPTY
// cache, or a cache holding rows adopted from the persisted record and not yet
// reconciled — in every such case the caller hydrates the session nodes off the wire.
//
// GATED LIKE ITS TWINS, for the same reason ChargeSnapshot is: three projections of
// one map, so a gate on one of them is a gate on none.
func (p *PropagationLoop) SessionSnapshot() ([]*knowledgev1.Node, bool) {
	if p == nil || p.corpus == nil {
		return nil, false
	}
	if p.corpusUnvalidated.Load() {
		return nil, false // adopted from disk, not yet validated — caller hydrates.
	}
	all := p.corpus.Snapshot()
	if len(all) == 0 {
		return nil, false // cold / just-Reset — not warm; caller hydrates.
	}
	if p.corpusUnvalidated.Load() {
		return nil, false // an adopt landed mid-read — the rows in hand are unvalidated.
	}
	sessions := make([]*knowledgev1.Node, 0, len(all))
	for _, n := range all {
		if kgtypes.NodeType(n.GetType()) == kgtypes.NodeThoughtSession {
			sessions = append(sessions, n)
		}
	}
	return sessions, true
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
	// The once-per-process warm-start adoption runs BEFORE cold is computed, so the
	// merged log line's `cold` field reports the drain shape truthfully: a
	// warm-loaded tick is not cold, and its horizon age reads as a single-page tick.
	adopted := p.warmLoadCorpusOnce()
	cold := p.corpus.Len() == 0
	final, pages, items, err := p.drainCorpusDelta(ctx)
	if err != nil {
		if adopted {
			// SCOPED TO THE ADOPTING TICK. The just-adopted cache has never been
			// probed, so a drain error leaves nothing that could validate it —
			// destroy it and return to the cold behavior this record exists to
			// make the exception. The flag stays SET; the cache is empty now, and
			// the next tick's successful reconcile clears it.
			p.corpus.Reset()
			slog.Warn("thought: corpus delta drain failed on the warm-start tick — discarded the unvalidated persisted cache", "err", err)
			return
		}
		// An ordinary warm tick keeps its previously-RECONCILED cache: that cache
		// already passed a probe, and the wire failing does not unprove it.
		slog.Warn("thought: corpus delta drain failed — cache not refreshed this tick", "err", err)
		return
	}
	// Reconcile at the pinned H against the final page's per-layer probes. A
	// mismatch is the PRODUCTION-RED cache-correctness check: reset + re-drain.
	if !p.corpus.Reconcile(final) {
		// REMOVE THE RECORD FIRST, unconditionally, and re-save only if the resync
		// both succeeds and reconciles. A crash in between therefore leaves NO
		// record and the next start is a plain cold drain — the correct bounded
		// disposition. Leaving the known-divergent record instead would make every
		// subsequent restart pay adopt + failed reconcile + full re-drain forever.
		removed := p.removePersistedCorpusRecord()
		slog.Warn("thought: corpus cache reconciliation mismatch — forced full resync",
			"safe_horizon", final.GetSafeHorizon(), "delta_items", items, "pages", pages,
			"removed_persisted", removed)
		p.corpus.Reset()
		// After the Reset there is no disk-derived data left to withhold, and the
		// empty cache reports cold on its own.
		p.corpusUnvalidated.Store(false)
		resyncFinal, _, resyncItems, rerr := p.drainCorpusDelta(ctx)
		if rerr != nil {
			slog.Warn("thought: corpus cache forced resync drain failed", "err", rerr)
			return
		}
		// RECONCILE THE RESYNC BEFORE TRUSTING IT. A full drain from zero cursors is
		// not self-evidently correct — it is the state the first Reconcile just
		// rejected, re-fetched. The final page is already in hand and Reconcile is a
		// pure in-memory comparison, so the check costs no round trip. Persisting an
		// unreconciled cache would put bytes on disk that the next process adopts and
		// then has to reject, which is the loop this arm exists to break.
		if !p.corpus.Reconcile(resyncFinal) {
			slog.Warn("thought: corpus cache forced resync did not reconcile either — no record written",
				"safe_horizon", resyncFinal.GetSafeHorizon(), "resync_items", resyncItems)
			return
		}
		if resyncItems > 0 {
			p.persistCorpusCache()
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
	// Reconciliation against this tick's probe is what makes the cache trustworthy,
	// so the flag clears UNCONDITIONALLY here while the persist stays conditional. A
	// zero-item tick is still a reconciled one: gating the clear on items > 0 would
	// strand an adopted-and-validated cache invisible forever on the common case
	// where nothing changed while the daemon was down.
	p.corpusUnvalidated.Store(false)
	// items > 0 is the right persist trigger, and not a timer: NextCursors only
	// advance on pages that returned rows (the server echoes the incoming cursor
	// unchanged for an empty page), so a zero-item tick changes neither the live set
	// nor the cursors and the on-disk record is already identical. A cold drain over
	// a non-empty corpus always has items > 0, so the first tick after a wipe always
	// seeds the record; an EMPTY corpus writes no record at all, which is correct —
	// there is nothing to warm.
	if items > 0 {
		p.persistCorpusCache()
	}
}

// persistCorpusCache writes the resident cache and its per-layer cursors to the
// warm-start record. Called ONLY from paths where the cache has just reconciled.
//
// A persist failure NEVER fails the tick: the cache is still correct in memory, and
// all that is lost is the next process's warm start.
//
// Snapshot and Cursors are read under two SEPARATE acquisitions of the cache's own
// mutex. That is acceptable here and only here, because refreshCorpusCache is the
// sole writer of this cache and calls this synchronously between drains, so no merge
// can interleave the two reads. Both read the CACHE directly rather than the gated
// accessors, and must: the corpusUnvalidated flag withholds rows from consumers, not
// from the record, and this is called only after a reconcile.
func (p *PropagationLoop) persistCorpusCache() {
	if p == nil || p.corpusCachePath == "" || p.corpus == nil {
		return
	}
	if err := saveCorpusRecord(p.corpusCachePath, corpusNodeTypes, p.corpus.Snapshot(), p.corpus.Cursors()); err != nil {
		slog.Warn("thought: failed to persist the corpus cache", "err", err, "path", p.corpusCachePath)
	}
}

// removePersistedCorpusRecord deletes a record whose cache has been PROVEN divergent
// by a failed reconciliation, and reports whether a file was actually removed.
//
// This is not destroy-before-persist: the record's absence is exactly the state its
// divergence calls for (a cold drain), and it can be regenerated from the server on
// any tick. An absent record is the intended end state, so a not-exist error is
// success rather than a fault.
func (p *PropagationLoop) removePersistedCorpusRecord() bool {
	if p == nil || p.corpusCachePath == "" {
		return false
	}
	if err := os.Remove(p.corpusCachePath); err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("thought: failed to remove the divergent persisted corpus cache",
				"err", err, "path", p.corpusCachePath)
		}
		return false
	}
	return true
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
