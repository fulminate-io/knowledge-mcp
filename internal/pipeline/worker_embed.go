// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/llm"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// runEmbedWorker pulls embed batches and processes them with panic recovery.
func runEmbedWorker(ctx context.Context, p *Pipeline, batchIn <-chan []EmbedWork, wg *sync.WaitGroup) {
	defer wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case batch, ok := <-batchIn:
			if !ok {
				return
			}
			runEmbedWorkerBatch(ctx, p, batch)
		}
	}
}

// runEmbedWorkerBatch is the embed-side mirror of runSummaryWorkerBatch.
//
// The recover defer is loop-safe for EVERY panic source, not just a logged
// warning: a recovered panic that wrote no durable marker would leave the
// batch's nodes embed-eligible, so the collector re-discovers + re-enqueues
// them and the SAME batch panics again — an infinite loop. So on recover we
// route every node in the panicked batch to the EXISTING terminal-marker path
// (markStuckEmbedItems) with a panic reason, mirroring the empty-text
// terminal-marker idiom documented at composeEmbedItems below ("Without the
// marker ... would loop the pipeline forever"). An unhandled panic in embed
// processing is a deterministic code bug — re-running panics again — so the
// marker is TERMINAL (human-clearable via clear_llm_failures), NOT a retry.
//
// Defer ordering (LIFO): the in-flight-release defer is registered AFTER the
// recover defer, so on a panic the release runs FIRST (in-flight is freed),
// then the recover defer stamps the terminal marker. The marker write is
// best-effort — an error only WARNs and never re-panics out of the recover.
func runEmbedWorkerBatch(ctx context.Context, p *Pipeline, batch []EmbedWork) {
	defer func() {
		if r := recover(); r != nil {
			slog.Warn("pipeline.embed worker: batch panic recovered — stamping terminal markers to break the eligibility loop",
				"panic", r, "batch_size", len(batch))
			markPanickedEmbedBatch(ctx, p, batch, r)
		}
	}()
	for range batch {
		p.metrics.embedRun()
	}
	defer func() {
		for _, w := range batch {
			p.metrics.embedDone()
			releaseInFlight(w.Release, w.NodeID)
		}
	}()

	groups := groupEmbedByGraph(batch)
	for key, items := range groups {
		processEmbedGroup(ctx, p, key, items)
	}
}

// markPanickedEmbedBatch stamps the durable terminal MetaKeyEmbedFailureReason
// marker on every node of a batch whose processing panicked (recovered above),
// so the eligibility loop terminates instead of re-discovering + re-panicking
// the same batch forever. Re-groups by (graphKey, Backend) so each marker write
// targets the right backend, and reuses the existing markStuckEmbedItems
// terminal-marker path (one mutate(update_batch) RPC per group). Best-effort:
// markStuckEmbedItems only WARNs on a write error, and this runs inside the
// recover so it must never panic — the wire call it makes is the same one the
// empty-text path already makes safely.
func markPanickedEmbedBatch(ctx context.Context, p *Pipeline, batch []EmbedWork, r any) {
	reason := fmt.Sprintf("embed batch panic recovered: %v", r)
	for key, items := range groupEmbedByGraph(batch) {
		ids := make([]string, 0, len(items))
		for _, w := range items {
			ids = append(ids, w.NodeID)
		}
		markEmbedItemsWithReason(ctx, p, backendOr(p, key.Backend), key.Key, ids, reason)
	}
}

// groupEmbedByGraph groups EmbedWork items by the COMPOSITE (graphKey, Backend)
// so each group is backend-homogeneous (see groupKey in worker.go).
func groupEmbedByGraph(batch []EmbedWork) map[groupKey][]EmbedWork {
	groups := make(map[groupKey][]EmbedWork)
	for _, w := range batch {
		k := groupKey{
			Key:     graphKey{GraphType: w.GraphType, GraphName: w.GraphName},
			Backend: w.Backend,
		}
		groups[k] = append(groups[k], w)
	}
	return groups
}

// processEmbedGroup handles one (gt, name) embed group: fetches nodes
// via wire, composes EmbedText, calls the embedder, writes binary vectors
// via ONE mutate(update_batch) RPC.
func processEmbedGroup(ctx context.Context, p *Pipeline, key groupKey, items []EmbedWork) {
	gk := key.Key
	be := backendOr(p, key.Backend)
	slog.Debug("pipeline.embed: processing group", "graph_type", gk.GraphType, "graph_name", gk.GraphName, "items", len(items))
	// Defense-in-depth against the nil-embedder loop: the embed axis is gated
	// OFF at Start / collector.run when p.embedder == nil, so no embed worker
	// should ever reach here without an embedder. This belt-and-suspenders guard
	// makes a future regression of that wiring gate fail SAFE — return without
	// calling the nil func (which would nil-panic) and WITHOUT stamping a marker,
	// leaving the nodes embed-ELIGIBLE for a later keyed run that does have an
	// embedder. (The terminal-marker recover path is for genuine panics, not the
	// no-embedder-configured case — those stay distinct.)
	if p.embedder == nil {
		slog.Debug("pipeline.embed: no embedder configured — group skipped (nodes stay embed-eligible)",
			"graph_type", gk.GraphType, "graph_name", gk.GraphName, "items", len(items))
		return
	}

	embedItems, idsForMarker := composeEmbedItems(ctx, p, be, gk, items)
	if len(embedItems) == 0 {
		slog.Debug("pipeline.embed: no items produced — batch skipped",
			"graph_type", gk.GraphType, "graph_name", gk.GraphName, "items", len(items))
		return
	}

	// Block here while the EMBED circuit breaker is latched paused (a prior
	// embed-axis zero-success storm), proactively pace the outbound request rate
	// (no-op unless --embed-rpm set), THEN pause if a prior transient failure
	// opened the reactive backoff window. The summary axis has its own independent
	// breaker, so a paused summary axis does not gate embed work here. waitResumed
	// selects on ctx.Done(), so a worker parked here unblocks on Pipeline.Stop and
	// never hangs the worker WaitGroup. The RPM gate paces steady-state dispatch;
	// the backoff window only opens after a failure — all three must clear before
	// the embedder is called.
	p.embedCircuit.waitResumed(ctx)
	p.embedRPM.wait(ctx)
	p.backoff.wait(ctx)
	slog.Debug("pipeline.embed: invoking embedder", "items", len(embedItems), "graph_type", gk.GraphType, "graph_name", gk.GraphName)
	vectors, err := p.embedder(ctx, embedItems)
	if err != nil {
		slog.Warn("pipeline.embed: embedder call failed",
			"graph_type", gk.GraphType, "graph_name", gk.GraphName, "items", len(embedItems), "error", err)
		handleEmbedderError(ctx, p, be, gk, idsForMarker, err)
		return
	}
	// A successful embed call zeroes the EMBED axis's zero-success-window counter
	// (and clears its per-class tally) on its own breaker; the summary axis is
	// independent and unaffected. The shared backoff gate also clears here.
	p.embedCircuit.recordOK()
	p.backoff.ok()
	slog.Debug("pipeline.embed: embedder returned",
		"items", len(embedItems), "vectors", len(vectors), "graph_type", gk.GraphType, "graph_name", gk.GraphName)
	writeEmbedResults(ctx, p, be, gk, vectors, idsForMarker, items)
	slog.Debug("pipeline.embed: writeEmbedResults done",
		"items", len(embedItems), "graph_type", gk.GraphType, "graph_name", gk.GraphName)
}

// composeEmbedItems reads the SERVER-COMPOSED EmbedText from each EmbedWork
// straight into an EmbedItem — it no longer re-fetches the node or
// composes EmbedText client-side (the server already did so at the gap-scan
// site, zero extra read). An item whose server-composed EmbedText is
// whitespace-only is routed to markStuckEmbedItems instead of the embedder: the
// server deliberately EMITS empty-embed items (it does not drop them) so the
// client stamps the durable MetaKeyEmbedFailureReason marker here — the
// eligibility-loop circuit breaker. Without the marker, an eligibility/EmbedText
// mismatch (ShouldEmbed=true but EmbedText returns "" or "\n") would loop the
// pipeline forever.
func composeEmbedItems(ctx context.Context, p *Pipeline, be WireClient, key graphKey, items []EmbedWork) ([]EmbedItem, []string) {
	out := make([]EmbedItem, 0, len(items))
	ids := make([]string, 0, len(items))
	var stuck []string
	for _, w := range items {
		if strings.TrimSpace(w.EmbedText) == "" {
			stuck = append(stuck, w.NodeID)
			continue
		}
		out = append(out, EmbedItem{ID: w.NodeID, Text: w.EmbedText})
		ids = append(ids, w.NodeID)
	}
	if len(stuck) > 0 {
		markStuckEmbedItems(ctx, p, be, key, stuck)
	}
	return out, ids
}

// markStuckEmbedItems stamps MetaKeyEmbedFailureReason on every node whose
// SERVER-COMPOSED EmbedText was whitespace-only — eligibility-loop circuit
// breaker. The empty-text detection now reads the server-supplied EmbedText
// (EmbedWork.EmbedText) rather than composing locally; the durable marker +
// embedFail metric behavior is unchanged. Issued as ONE mutate(update_batch)
// RPC scoped to (graphType, graphName).
func markStuckEmbedItems(ctx context.Context, p *Pipeline, be WireClient, key graphKey, ids []string) {
	const reason = "embed-text-empty: ShouldEmbed=true but EmbedText returned whitespace-only"
	markEmbedItemsWithReason(ctx, p, be, key, ids, reason)
}

// markEmbedItemsWithReason is the shared durable terminal-marker write: it
// stamps MetaKeyEmbedFailureReason=reason on every id via ONE
// mutate(update_batch) RPC scoped to (graphType, graphName) and bumps embedFail
// per id. The eligibility-loop circuit breaker for ANY terminal embed
// condition — empty server-composed text (markStuckEmbedItems) and a recovered
// batch panic (markPanickedEmbedBatch) both route here. A write error only
// WARNs (best-effort): a missed marker re-surfaces the node next tick, which is
// strictly safer than blocking the worker.
func markEmbedItemsWithReason(ctx context.Context, p *Pipeline, be WireClient, key graphKey, ids []string, reason string) {
	slog.Warn("pipeline.embed: stamping terminal failure marker",
		"count", len(ids), "reason", reason, "graph_type", key.GraphType, "graph_name", key.GraphName)
	items := make([]updateBatchItem, 0, len(ids))
	for _, id := range ids {
		items = append(items, updateBatchItem{
			ID: id,
			Metadata: map[string]string{
				kgtypes.MetaKeyEmbedFailureReason: reason,
			},
		})
	}
	if err := writeBatchUpdates(ctx, be, key.GraphType, key.GraphName, items); err != nil {
		slog.Warn("pipeline.embed: stamp failure markers failed", "items", len(items), "error", err, "graph_type", key.GraphType, "graph_name", key.GraphName)
	}
	for range ids {
		p.metrics.embedFail()
	}
}

// writeEmbedResults writes each id's binary vector + clears the embed
// failure marker via ONE mutate(update_batch) RPC. ids carries the
// per-position node-ID order matching the embedder's output map keys. work is the
// originating EmbedWork slice — carried through so the ship seam can build BM25
// Documents from each item's server-composed Bm25Fields.
func writeEmbedResults(ctx context.Context, p *Pipeline, be WireClient, key graphKey, vectors map[string][]byte, ids []string, work []EmbedWork) {
	items := make([]updateBatchItem, 0, len(vectors))
	for _, id := range ids {
		v, ok := vectors[id]
		if !ok || len(v) == 0 {
			continue
		}
		items = append(items, updateBatchItem{
			ID:           id,
			BinaryVector: v,
			Metadata: map[string]string{
				kgtypes.MetaKeyEmbedFailureReason: "",
			},
		})
	}
	if len(items) == 0 {
		return
	}
	// Log the EXACT ids + the writeback target graph name so the gap's source
	// layer (logged at discovery) can be compared with where the vector is
	// actually written — a mismatch means the vector lands on a layer the next
	// gap-scan does not read, so the node loops forever.
	debugLogWriteback("embed", key.GraphName, items)
	if err := writeBatchUpdates(ctx, be, key.GraphType, key.GraphName, items); err != nil {
		slog.Warn("pipeline.embed: writeEmbedResults batch write failed", "error", err, "items", len(items), "graph_type", key.GraphType, "graph_name", key.GraphName)
		for range items {
			p.metrics.embedFail()
		}
		return
	}
	for range items {
		p.metrics.embedOK()
	}

	// BEST-EFFORT: also build + ship client-side segments from the freshly-embedded
	// binary vectors (HNSW) AND the server-composed per-field text (BM25) — the
	// client builds + ships. A failure here NEVER fails embed writeback: server-side
	// search is retired, so these client segments ARE the search index, but a
	// dropped ship only leaves those nodes briefly unsearchable until the next ship
	// — writeback liveness wins. WARN only.
	shipEmbedSegments(ctx, p, be, key, vectors, ids, work)
}

// shipEmbedSegments feeds the just-written binary vectors into the per-graph HNSW
// engine and the server-composed per-field BM25 text into the per-graph BM25 engine,
// shipping any newly-sealed segments of BOTH formats through the ONE dual-format
// Manager. No-op when no segment manager is wired (test fakes). Best-effort: any
// error is logged at WARN and swallowed — but the ids it dropped are STAMPED with
// a durable marker so the drop is attributable afterwards instead of surviving
// only as a log line.
func shipEmbedSegments(ctx context.Context, p *Pipeline, be WireClient, key graphKey, vectors map[string][]byte, ids []string, work []EmbedWork) {
	if p.segmentMgr == nil {
		return
	}
	shipEmbedHNSW(ctx, p, be, key, vectors, ids)
	shipEmbedBM25(ctx, p, be, key, vectors, work)
}

// stampShipFailure records, on every id a swallowed ship dropped, WHY it was
// dropped. Without it the node is embedded, absent from the searchable corpus, and
// indistinguishable from a healthy one — the state that makes a coverage hole
// untraceable.
//
// The stamp is itself best-effort: it can fail, and its failure is logged rather
// than propagated, for the same reason the ship it records is swallowed — embed
// writeback liveness wins. The marker key is deliberately NOT the embed-failure
// key, which both rebuild scans exclude; a ship-dropped node must stay scan-eligible
// for the repair that re-ships it.
func stampShipFailure(ctx context.Context, be WireClient, key graphKey, docs []searchengine.Document, reason string) {
	items := make([]updateBatchItem, 0, len(docs))
	for _, d := range docs {
		items = append(items, updateBatchItem{
			ID: d.ID,
			Metadata: map[string]string{
				kgtypes.MetaKeySegmentShipFailureReason: reason,
			},
		})
	}
	if werr := writeBatchUpdates(ctx, be, key.GraphType, key.GraphName, items); werr != nil {
		slog.Warn("pipeline.embed: write segment-ship failure markers failed",
			"items", len(items), "error", werr, "graph_type", key.GraphType, "graph_name", key.GraphName)
	}
}

// shipEmbedHNSW builds + ships the HNSW segment from the binary vectors.
func shipEmbedHNSW(ctx context.Context, p *Pipeline, be WireClient, key graphKey, vectors map[string][]byte, ids []string) {
	docs := BuildHNSWDocuments(vectors, ids)
	if len(docs) == 0 {
		return
	}
	if err := p.segmentMgr.AddAndMarkDirty(ctx, key.GraphType, key.GraphName, docs); err != nil {
		slog.Warn("pipeline.embed: client HNSW build+ship failed (additive/best-effort; server vector path authoritative)",
			"error", err, "docs", len(docs), "graph_type", key.GraphType, "graph_name", key.GraphName)
		// The reason names the FORMAT as well as the error, so the two ship sites
		// are distinguishable from the marker alone.
		stampShipFailure(ctx, be, key, docs, fmt.Sprintf("hnsw ship dropped: %v", err))
	}
}

// shipEmbedBM25 builds + ships the BM25 segment from each item's server-composed
// Bm25Fields. Only items that BOTH got embedded (a vector landed
// — so the node is genuinely live) AND carry non-empty fields are indexed. A
// code-leaf embedded via Content before its summary/keywords land carries a thin
// (possibly summary-less) Bm25Fields — that is ACCEPTABLE: it self-heals when
// re-summarization bumps the embed dirty-gen and re-ships, so a transient thin
// segment only under-indexes that one node for the brief window until it re-ships.
func shipEmbedBM25(ctx context.Context, p *Pipeline, be WireClient, key graphKey, vectors map[string][]byte, work []EmbedWork) {
	// Map []EmbedWork → []SegmentDoc, preserving the "only index nodes that
	// actually embedded this tick" vector-presence gate before delegating the
	// doc assembly to the shared builder.
	segDocs := make([]SegmentDoc, 0, len(work))
	for _, w := range work {
		if v, ok := vectors[w.NodeID]; !ok || len(v) == 0 {
			continue // only index nodes that actually embedded this tick
		}
		segDocs = append(segDocs, SegmentDoc{NodeID: w.NodeID, Fields: w.Bm25Fields})
	}
	docs := BuildBM25Documents(segDocs)
	if len(docs) == 0 {
		return
	}
	if err := p.segmentMgr.AddAndMarkDirtyFields(ctx, key.GraphType, key.GraphName, docs); err != nil {
		slog.Warn("pipeline.embed: client BM25 build+ship failed (additive/best-effort; server BM25 path authoritative)",
			"error", err, "docs", len(docs), "graph_type", key.GraphType, "graph_name", key.GraphName)
		stampShipFailure(ctx, be, key, docs, fmt.Sprintf("bm25 ship dropped: %v", err))
	}
}

// handleEmbedderError applies the transient/terminal classification per id.
// Both branches log at WARN so the operator sees the actual error in the
// log immediately. Terminal errors stamp the failure marker on every id
// via ONE mutate(update_batch) RPC.
func handleEmbedderError(ctx context.Context, p *Pipeline, be WireClient, key graphKey, ids []string, err error) {
	// Every errored embedder call — transient OR terminal — feeds the EMBED
	// breaker's zero-success window: only an actual embed success (recorded at the
	// recordOK() site) resets it, so a full round where every embed call errors
	// trips the EMBED breaker (the summary axis is independent). classify(err)
	// tallies the error's class so the auto-trip reason names the dominant class.
	// On an auto-trip THIS call caused, hand off to the shared-cause escalation
	// coordinator, which cross-trips the summary axis ONLY when the dominant class
	// is auth/quota AND both axes share the same provider.
	if p.embedCircuit.recordErr(classify(err)) {
		p.escalateOnTrip(embedBreakerAxis)
	}
	if llm.IsTransient(err) {
		// Honor a provider 429/503 Retry-After when present (else exponential).
		hint := llm.RetryAfterOf(err)
		delay := p.backoff.failHint(hint)
		slog.Warn("pipeline.embed: transient error, backing off before retry",
			"items", len(ids), "backoff", delay, "retry_after_hint", hint, "error", err)
		for range ids {
			p.metrics.embedFail()
		}
		return
	}

	reason := err.Error()
	if llmErr, ok := errors.AsType[*llm.LLMError](err); ok {
		reason = llmErr.Error()
	}

	slog.Warn("pipeline.embed: terminal error, marking nodes as failed",
		"items", len(ids), "reason", reason, "error", err)

	items := make([]updateBatchItem, 0, len(ids))
	for _, id := range ids {
		items = append(items, updateBatchItem{
			ID: id,
			Metadata: map[string]string{
				kgtypes.MetaKeyEmbedFailureReason: reason,
			},
		})
	}
	if werr := writeBatchUpdates(ctx, be, key.GraphType, key.GraphName, items); werr != nil {
		slog.Warn("pipeline.embed: write failure markers failed",
			"items", len(items), "error", werr, "graph_type", key.GraphType, "graph_name", key.GraphName)
	}
	for range ids {
		p.metrics.embedFail()
	}
}
