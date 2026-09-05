// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"log/slog"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// collector_bm25.go is the BM25 arm: a THIRD per-graph loop beside the summary and
// embed axes, producing BM25 segment documents from the CorpusDelta change feed
// instead of as a side effect of embedding.
//
// IT HAS NO PENDING SET AND NO COMPLETION ACK. A durable per-graph cursor replaces
// both: the arm drains what is past its cursor, ships it, and only then advances.
// That is why it does not reuse runLoop — that loop is built around PipelineScan's
// item / in-flight / release protocol, which this arm does not have.
//
// The arm's per-collector state lives in ONE struct referenced by a single field in
// collector.go, because that file sits against an enforced 500-line commit gate.

// CorpusDeltaScanner is the package-local CorpusDelta seam the BM25 arm pages its
// drain through — a twin of the thought package's identically-shaped interface, and
// declared here for the reason that one gives for its own: the wire contract is the
// generated proto, not a shared Go type.
//
// IT IS DELIBERATELY NOT A FOURTH METHOD ON WireClient. That interface is satisfied
// by many in-package test fakes, and widening it would tax every one of them for a
// method almost none will call. The production routedWireClient already has
// CorpusDelta, so it satisfies this seam with no change.
type CorpusDeltaScanner interface {
	CorpusDelta(ctx context.Context, req *knowledgev1.CorpusDeltaRequest) (*knowledgev1.CorpusDeltaResponse, error)
}

// bm25DeltaPageSize is the arm's per-page cap, matching the thought corpus drain's.
// A change-gated tick's page is tiny, so one short page terminates it; a burst
// drains in ceil(M/pageSize) pages, all anchored to page 1's pinned horizon.
const bm25DeltaPageSize = 500

// bm25Arm is the arm's whole per-collector state, grouped into ONE struct so
// collector.go carries a single new field against its 500-line budget.
//
// EVERY FUNC FIELD IS A CLOSURE BUILT IN RegisterGraph, exactly as flush and
// healIfSegmentless are, so the collector keeps NO segmentdist dependency — it sees
// only these funcs.
type bm25Arm struct {
	// enabled is set at construction: a segment manager is attached AND the graph
	// type has rebuildable segments. A disabled arm starts no loop at all, which is
	// the ABSENCE of a feature rather than a degraded lane.
	enabled bool
	// wake is buffered(1) so one WakeAll reaches all three loops.
	wake chan struct{}
	// loadCursors / saveCursors are the durable per-graph position.
	loadCursors func() ([]*knowledgev1.LayerCursor, error)
	saveCursors func([]*knowledgev1.LayerCursor) error
	// corpusStamp reports the server's per-graph maximum node updated_at and whether
	// the central gen-poll has sampled this graph yet. It is the O(1)
	// has-anything-changed signal the gate below compares against the cursor.
	corpusStamp func() (stamp int64, sampled bool)
	// ship seals BM25 segments from the page's documents and marks them for re-emit.
	ship func(ctx context.Context, docs []searchengine.Document) error
	// deleteIDs removes tombstoned ids from their routed buckets, which is what makes
	// a delete reach the durable blob rather than only the in-memory live set.
	deleteIDs func(ctx context.Context, ids []searchengine.ExternalID) error
}

// cursorHighWater is the arm's position as ONE comparable number: the maximum
// after_updated_at across its per-layer cursors.
//
// A MAXIMUM IS THE RIGHT FOLD because the server's stamp is itself a maximum over
// the same population. The arm requests ALL node types, so every row the feed can
// serve advances some layer's cursor — which is what lets the two numbers converge
// and the gate close. Were the arm to request a SUBSET of types, a change to an
// unrequested type would raise the stamp forever without ever advancing a cursor,
// and the gate would admit a walk on every tick. That is why all_node_types and
// this fold are one design and not two choices.
func cursorHighWater(cursors []*knowledgev1.LayerCursor) int64 {
	var high int64
	for _, c := range cursors {
		if c.GetAfterUpdatedAt() > high {
			high = c.GetAfterUpdatedAt()
		}
	}
	return high
}

// skipQuiescentGraph reports whether this tick can skip the CorpusDelta request
// entirely — the O(1) gate.
//
// THIS IS THE CONDITION THE WALK RULING TURNS ON. The measured steady-state
// CorpusDelta page costs a full-layer walk (34ms at 500k nodes against a flat
// ~380ns embed-axis baseline), and that cost was accepted FOR CHANGE PROCESSING
// ONLY. An ungated per-tick walk must not ship, so a quiescent graph must issue no
// request at all.
//
// IT FAILS OPEN, DELIBERATELY, in the one case that is ambiguous: when the central
// poll has not sampled this graph yet (sampled == false) the arm DRAINS. Treating
// "not yet polled" as "nothing changed" would keep a freshly-registered graph out
// of the BM25 corpus until some unrelated event happened to poll it — silent
// under-indexing, which is the failure this whole ticket removes. A zero stamp WITH
// sampled == true is different and correctly skips: that is the server's honest
// "never recorded", and an empty graph has nothing to drain.
//
// A FRESHLY-COLLECTED RAW GRAPH IS EXACTLY THE CASE THIS FAIL-OPEN IS FOR, and it
// needs no change to serve one. WHAT IT DOES NOT SURVIVE ON ITS OWN IS A DROPPED
// GRAPH: a dropped graph presents the SAME never-sampled state, so this arm would
// drain it forever, paying a full-layer walk every tick for a graph that no longer
// exists. The gate that prevents that is the working-set REMOVAL on the drop path
// (workingset.Remove, called from the drop handler): the pipeline's wanted set is a
// filter over working-set members, so a removed graph never starts an arm and never
// reaches this predicate. The two are coupled — do not remove one without the other.
func (c *collector) skipQuiescentGraph(cursors []*knowledgev1.LayerCursor) bool {
	if c.bm25.corpusStamp == nil {
		return false // no signal wired — drain rather than skip, same fail-open rule.
	}
	stamp, sampled := c.bm25.corpusStamp()
	if !sampled {
		return false
	}
	high := cursorHighWater(cursors)
	if stamp > high {
		return false
	}
	slog.Debug("pipeline.collector: BM25 change gate held — no corpus change past this arm's cursor",
		"graph_type", c.gt, "name", c.name, "server_stamp", stamp, "cursor_high_water", high)
	return true
}

// runBM25Loop is the arm's discovery cycle. It shares the two LLM loops' throttles
// by calling the same helpers — sleepForWake, nextIdleInterval, errBackoff and the
// collect-gate predicate — while keeping its own control flow, because runLoop's
// body is PipelineScan's in-flight/release protocol and this arm has neither.
//
// Exits on ctx.Done.
func (c *collector) runBM25Loop(ctx context.Context) {
	// NO SILENT DEGRADATION. An enabled arm whose client cannot serve CorpusDelta is
	// a WIRING DEFECT, not a mode: the graph would produce no BM25 documents at all
	// and nothing would say so. Fail loudly, naming the graph and the capability.
	scanner, ok := c.client.(CorpusDeltaScanner)
	if !ok {
		slog.Error("pipeline.collector: BM25 arm is enabled but its backend cannot serve CorpusDelta — "+
			"this graph will produce NO BM25 documents; the arm is not starting",
			"graph_type", c.gt, "name", c.name)
		return
	}

	base := c.baseTick
	if base <= 0 {
		base = c.cfg.TickOrDefault()
	}
	idleMax := max(c.idleTick, base)
	interval := base
	backoff := newErrBackoff(c.cfg.ErrBackoffBaseOrDefault(), c.cfg.ErrBackoffMaxOrDefault())
	// pendingSinceFlush mirrors the embed axis's loop-LOCAL latch: it latches while a
	// drain is moving rows and fires the flush ONCE on the drain-complete edge, so the
	// post-drain idle ticks do not re-fire it.
	pendingSinceFlush := false

	for {
		// #4 collect-gate, same predicate and same reasoning as the LLM loops: while a
		// collect into this graph is in flight its rows are still landing, and scanning
		// now puts this loop's writes in the way of the collect's finalize.
		if c.collectInFlight != nil && c.collectInFlight() {
			if !c.sleepFor(ctx, base) {
				return
			}
			continue
		}

		cursors, err := c.bm25.loadCursors()
		if err != nil {
			// The position is unreadable, so a drain would re-ship from zero and an
			// advance would write over a position we could not read. Back off instead.
			d := backoff.failHint(0)
			slog.Warn("pipeline.collector: BM25 arm could not read its cursor; backing off",
				"graph_type", c.gt, "name", c.name, "delay", d, "error", err)
			if !c.sleepFor(ctx, d) {
				return
			}
			continue
		}

		// The quiescence gate is written INVERTED so the drain is the only nested
		// branch: when the gate HOLDS, no CorpusDelta request is issued, no walk is
		// paid, and `merged` stays zero. The flush edge below sits outside the branch
		// because it evaluates on BOTH paths — so a drain that ended on the previous
		// tick still seals its tail on a quiescent tick, which is the same flush the
		// gate-held branch used to make with a literal zero.
		merged := 0
		if !c.skipQuiescentGraph(cursors) {
			merged, err = c.drainBM25(ctx, scanner, cursors)
			if err != nil {
				if !c.backoffAfterBM25DrainFailure(ctx, backoff, cursors, err) {
					return
				}
				continue
			}
			backoff.ok()
		}
		pendingSinceFlush = c.maybeBM25Flush(ctx, merged, pendingSinceFlush)

		// #1 idle-backoff: work found → fast base cadence; nothing → grow toward
		// idleMax. A collect wake cuts the idle sleep short.
		if merged > 0 {
			interval = base
		} else {
			interval = nextIdleInterval(interval, idleMax)
		}
		alive, _ := c.sleepForWake(ctx, interval, c.bm25.wake)
		if !alive {
			return
		}
	}
}

// backoffAfterBM25DrainFailure reports a failed BM25 drain and sleeps the arm's
// error backoff. It reports whether the loop is still ALIVE: false means the
// context ended during the sleep and the caller must return, true means the
// caller should re-enter the loop and re-drain the held page.
//
// The cursor is deliberately NOT advanced anywhere on this path — it is HELD, so
// the same page is re-drained and re-shipped next tick, and the ship is keyed by
// document id so the re-ship is an idempotent re-seal rather than a duplicate.
func (c *collector) backoffAfterBM25DrainFailure(
	ctx context.Context, backoff *errBackoff, cursors []*knowledgev1.LayerCursor, err error,
) bool {
	d := backoff.failHint(0)
	slog.Warn("pipeline.collector: BM25 drain failed; the cursor is HELD so the same page "+
		"is re-drained and re-shipped next tick",
		"graph_type", c.gt, "name", c.name, "cursor_high_water", cursorHighWater(cursors),
		"delay", d, "error", err)
	return c.sleepFor(ctx, d)
}

// drainBM25 pages CorpusDelta from the arm's cursors to exhaustion, shipping each
// page and advancing the cursor ONLY after the page has landed. Returns the number
// of documents shipped.
//
// THE PINNED HORIZON IS PAGE 1'S, and the reason is the same one the thought
// corpus drain gives: H is non-monotonic, so recomputing it per page could regress
// it below page 1's cursor mid-drain, yielding an empty page while rows sit
// stranded above the cursor.
//
// THE ORDER IS THE CORRECTNESS PROPERTY: ship, then land deletes, then advance.
// A cursor advanced past an unshipped page converts a transient ship failure into
// permanently unindexed nodes, so the advance is the LAST thing that happens and it
// is not deferred — a deferred save would run on the error path too, which is the
// defect the standing go practice annotation for this shape names.
//
// A FAILED PAGE HOLDS THE CURSOR AND RETURNS. The next tick re-drains the same page
// and re-ships it; the ship is keyed by document id, so a re-ship is an idempotent
// re-seal rather than a duplicate.
func (c *collector) drainBM25(
	ctx context.Context, scanner CorpusDeltaScanner, cursors []*knowledgev1.LayerCursor,
) (int, error) {
	pinned := int64(0)
	shipped := 0
	for {
		resp, err := scanner.CorpusDelta(ctx, &knowledgev1.CorpusDeltaRequest{
			GraphType: string(c.gt),
			GraphName: c.name,
			// ALL TYPES, not a filter: an EMPTY node_types is the thought-corpus triple
			// on both backends rather than "everything", so a filtered request would
			// serve a code graph nothing at all. It is also what makes the gate's
			// cursor/stamp comparison converge — see cursorHighWater.
			AllNodeTypes:      true,
			ComposeBm25Fields: true,
			Cursors:           cursors,
			Limit:             bm25DeltaPageSize,
			PinnedHorizon:     pinned,
		})
		if err != nil {
			return shipped, err
		}
		if pinned == 0 {
			pinned = resp.GetSafeHorizon()
		}

		docs, deleted := partitionBM25Page(resp)
		if len(docs) > 0 {
			if err := c.bm25.ship(ctx, docs); err != nil {
				return shipped, err
			}
			shipped += len(docs)
		}
		if len(deleted) > 0 {
			if err := c.bm25.deleteIDs(ctx, deleted); err != nil {
				// The cursor is NOT advanced, so this window's deletes are re-served
				// next tick. Losing a delete leaves a removed node searchable forever.
				return shipped, err
			}
		}

		// ONLY NOW. Everything this page carried is durable-or-retried above.
		cursors = resp.GetNextCursors()
		if err := c.bm25.saveCursors(cursors); err != nil {
			return shipped, err
		}
		if len(resp.GetItems()) < bm25DeltaPageSize {
			return shipped, nil // short/empty final page — drain exhausted.
		}
	}
}

// partitionBM25Page splits one served page into the documents to seal and the ids
// to remove.
//
// A TOMBSTONED ROW IS A DELETE AND CARRIES NO DOCUMENT. The server sends no
// bm25_items entry for one, so the split is on the item's own tombstoned_at rather
// than on the absence of a composed entry — an absent entry also means "composed to
// nothing", which is a live row with no indexable text and must NOT be deleted.
func partitionBM25Page(resp *knowledgev1.CorpusDeltaResponse) ([]searchengine.Document, []searchengine.ExternalID) {
	fields := make(map[string]*knowledgev1.Bm25Fields, len(resp.GetBm25Items()))
	for _, it := range resp.GetBm25Items() {
		fields[it.GetNodeId()] = it.GetFields()
	}
	items := make([]SegmentDoc, 0, len(resp.GetItems()))
	var deleted []searchengine.ExternalID
	for _, n := range resp.GetItems() {
		if n.GetTombstonedAt() != 0 {
			deleted = append(deleted, n.GetId())
			continue
		}
		f, ok := fields[n.GetId()]
		if !ok {
			continue // composed to nothing — a live row with no indexable text.
		}
		items = append(items, SegmentDoc{NodeID: n.GetId(), Fields: bm25FieldsFromProto(f)})
	}
	return BuildBM25Documents(items), deleted
}

// maybeBM25Flush is the arm's quiescence edge, the same shape as the embed axis's
// latch and for the same reason: a graph with fewer than MinSegmentDocs newly
// changed nodes seals no segment until something force-seals the tail, and after
// decoupling a fully-embedded graph has no embed work to carry that edge.
//
// IT CALLS THE SAME FLUSH CLOSURE, NOT A PER-FORMAT SPLIT. Manager.Flush seals both
// formats and each leg is independently gated on having unwritten export, so its own
// doc records that a no-progress re-Flush is a true no-op — the quiet format costs a
// gate check. A per-arm flush would double the surface and buy nothing.
func (c *collector) maybeBM25Flush(ctx context.Context, merged int, pending bool) bool {
	if merged > 0 {
		return true
	}
	if !pending || c.flush == nil {
		return pending
	}
	slog.Info("pipeline.collector: BM25 drain complete — quiescence flush (force-seal sub-threshold tail)",
		"graph_type", c.gt, "name", c.name)
	if err := c.flush(ctx); err != nil {
		slog.Warn("pipeline.collector: BM25 quiescence flush failed (best-effort; next drain retries)",
			"graph_type", c.gt, "name", c.name, "error", err)
	}
	return false
}

// bm25ArmEnabledFor is the arm's GRAPH GATE, and it is an EXISTING predicate rather
// than a new one: kgtypes.HasRebuildableSegments resolves to exactly {knowledge,
// code, cloud, cicd, practice, checks, web, pdf} and is already the gate
// manage(status)'s coverage probe and the heal factory use.
//
// THE TWO RAW GRAPHS ARE IN THAT SET DELIBERATELY, and this arm is why: server-side
// BM25 segments over the collected chunks are what make a raw web or pdf graph
// keyword-searchable at all, which replaced a client-side whole-graph drain-and-rank.
//
// FAIL-CLOSED BY CONSTRUCTION, which is the point, and the argument is unchanged by
// the widening. linkage and logs are outside the segment world entirely — linkage
// holds proxy edges with no text, logs are never embedded. A hand-rolled predicate
// like "BM25-eligible = not embeddable" would silently grant a non-embeddable
// family segments it must not have. Reusing the repo's allowlist is what
// discharges the standing cross-graph requirement rather than re-deciding it here.
//
// CHECKS IS ADMITTED HERE AND NARROWED SERVER-SIDE. The graph carries segments for
// its check findings; its fixture example nodes — code authored deliberately to be
// wrong — are refused by the server's per-graph node-type allow-list before they
// ever reach a document feed, so the exclusion is not this gate's job.
func bm25ArmEnabledFor(gt kgtypes.GraphType, hasManager bool) bool {
	return hasManager && kgtypes.HasRebuildableSegments(gt)
}
