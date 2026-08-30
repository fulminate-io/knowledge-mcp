// SPDX-License-Identifier: Apache-2.0

// write_context_links.go — the single client-side home for the write-side
// context-linking enablers. Its consumers are the type-keyed mutate-create
// handlers, record_decision, the think path, and the type-blind create arm that
// serves every remaining node type:
// every knowledge-graph create routes the context-link trio, and it routes it
// through here. The helper lowers the optional ticket_id / session / links
// pass-through params onto a create_batch so a session-produced node is BORN
// linked to the active ticket, grouped under its session, and related to the
// touched code/knowledge nodes — using only the existing edge vocabulary
// (contains / relates-to).
//
// FAIL-TOLERANCE IS THE CONTRACT (the crux): a context link must NEVER
// block the write it decorates. applyCreate (engine_mutate_apply.go) resolves
// every Idx==-1 existing-node edge endpoint via resolveWriteID and aborts the
// WHOLE create_batch on a missing target — so an unresolvable ticket_id or a
// knowledge-target link ID riding the batch would block the node write, the
// exact thing the ticket forbids. The fix is to PRE-VALIDATE every knowledge-
// graph endpoint via LookupNode BEFORE the batch is built (the
// validateInformedByRefs idiom): a resolvable target rides the atomic batch; an
// absent one is DROPPED with a warning. Code/cloud (foreign) link targets
// cannot ride a knowledge create_batch at all, so they are returned for a
// post-create LinkOne (which the caller log-and-continues on error).
//
// LINK CLASSIFICATION (single-probe, mirrors resolveCrossGraphID a/b/c):
//   (a) ID resolves in the knowledge graph  → knowledge target: rides the batch
//       as node--relates-to-->id (pre-validated by the very probe).
//   (b) ID resolves only via a foreign graph → code target: upsert a knowledge
//       proxy, return the proxy id for a post-create LinkOne (log+continue).
//   (c) ID resolves nowhere                  → dropped with a warning.
//
// MEASURING THE EFFECT (links-per-created-node edge density) — an honest,
// reproducible procedure, no new analyzer or metric infrastructure (both readers
// already exist):
//
//   - BASELINE / DENOMINATOR (corpus-wide only): query(mode:stats,
//     graph:knowledge) reports graph-WIDE totals — e.g. a baseline of
//     ~638 informed-by edges over ~80,130 nodes. Use stats ONLY for the
//     corpus-wide baseline rate; it CANNOT answer the per-session numerator
//     (it has no session/ticket scoping).
//
//   - PER-SESSION NUMERATOR (the real reader): to measure links-per-created-node
//     for the nodes produced within one session or ticket, enumerate the member
//     nodes with a depth-1 contains traversal from that node —
//       traverse({start: <session_or_ticket_id>, graph: "knowledge",
//                 edge_types: ["contains"], direction: "out", depth: 1})
//     then for each returned member count its incident context links (the
//     ticket/session contains + relates-to edges this helper emits).
//     links-per-created-node = (total such links across members) / (member count).
//
// Stats for the denominator/baseline; the contains-traversal for the per-session
// numerator. Before this enabler, session-produced finding/research/rule/decision
// nodes carried zero context edges; after it, a resolvable ticket_id/session/links
// raises that numerator at the source.

package tools

import (
	"context"
	"fmt"

	"github.com/fulminate-io/knowledge-mcp/internal/crossgraph"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
	"github.com/fulminate-io/knowledge-mcp/internal/projects/render"
)

// contextLinks is the lowered result of the ticket_id / session / links
// pass-through params. batchEdges decorate the create_batch (every edge points
// at the new node's slot, supplied by the caller as nodeSlot); codeLinks are
// foreign-proxy IDs the caller LinkOnes AFTER the create (the new node ID is not
// known until the batch returns); warnings record every dropped target.
type contextLinks struct {
	batchEdges []kgwire.BatchEdge
	codeLinks  []string
	warnings   []string
}

// contextNodeSlot is the create_batch slot index of the node being decorated
// with context edges. Every caller of buildContextLinks builds a single node and
// places it at slot 0, so the contains/relates-to edges originate from / point at
// slot 0. Named as a constant rather than a parameter because no caller varies it.
const contextNodeSlot = 0

// buildContextLinks pre-validates the ticket_id / session / links context params
// and lowers them into a contextLinks result for the create paths. The node being
// created is always batch slot contextNodeSlot (0) for the single-node creates
// that call this.
//
// Every resolution is fail-tolerant: an unresolvable ticket_id, a session
// resolve/create error, and a link target that resolves nowhere each DROP the
// edge and append a warning — the create itself is never aborted. Session
// resolution reuses getOrCreateThoughtSessionClient (the think-path
// get-or-create), so a brand-new session is created on demand and the
// session--contains edge is always resolvable on the happy path.
func buildContextLinks(ctx context.Context, gc GraphCaller, ticketID, session string, links []string) contextLinks {
	var cl contextLinks

	// (1) ticket_id → ticket--contains-->node. Pre-validate via LookupNode so an
	// absent ticket drops+warns BEFORE the batch is built (an Idx==-1 FromID that
	// resolveWriteID cannot resolve would abort the whole create_batch).
	if ticketID != "" {
		n, err := LookupNode(ctx, gc, ticketID)
		if err != nil || n == nil {
			cl.warnings = append(cl.warnings, fmt.Sprintf("ticket_id %q not found — the ticket link was dropped (the node was still created)", ticketID))
		} else {
			cl.batchEdges = append(cl.batchEdges, kgwire.BatchEdge{FromIdx: -1, FromID: ticketID, ToIdx: contextNodeSlot, Type: kgtypes.EdgeKGContains})
		}
	}

	// (2) session → session--contains-->node. getOrCreateThoughtSessionClient
	// CREATES the session when absent, so a resolve/create error (not a
	// not-found) is the only failure mode — drop+warn, never block.
	if session != "" {
		sid, serr := getOrCreateThoughtSessionClient(ctx, gc, session)
		if serr != nil || sid == "" {
			cl.warnings = append(cl.warnings, fmt.Sprintf("session %q could not be resolved/created — the session link was dropped (the node was still created)", session))
		} else {
			cl.batchEdges = append(cl.batchEdges, kgwire.BatchEdge{FromIdx: -1, FromID: sid, ToIdx: contextNodeSlot, Type: kgtypes.EdgeKGContains})
		}
	}

	// (3) links → node--relates-to-->target. Classify each id single-probe:
	// knowledge hit rides the batch (pre-validated); foreign hit becomes a
	// post-create code link; no hit drops+warns.
	cl.classifyLinks(ctx, gc, links)
	return cl
}

// classifyLinks resolves each link id into the knowledge-target (batch) vs
// code-target (post-create) vs dropped buckets, mutating cl in place. The
// foreign-graph list is enumerated ONCE and reused across every id (the
// resolveThinkLinks idiom).
func (cl *contextLinks) classifyLinks(ctx context.Context, gc GraphCaller, links []string) {
	if len(links) == 0 {
		return
	}
	ex, _ := persistExecutor(gc)
	var graphs []crossgraph.ForeignGraph
	if ex != nil {
		graphs, _ = crossgraph.ListForeignGraphs(ctx, ex)
	}
	for i, id := range links {
		if id == "" {
			// DROP AND WARN, like every other drop path in this helper. The entry
			// is still dropped and the create still succeeds; the index is named
			// so a caller can find which entry of their array was empty.
			cl.warnings = append(cl.warnings, fmt.Sprintf("links[%d] was empty — the relates-to link was dropped (the node was still created)", i))
			continue
		}
		// (a) Knowledge hit → knowledge target rides the batch as relates-to.
		if known, ferr := render.FetchNodeIn(ctx, gc, id, "knowledge", ""); ferr == nil && known != nil && known.Id != "" {
			cl.batchEdges = append(cl.batchEdges, kgwire.BatchEdge{FromIdx: contextNodeSlot, ToIdx: -1, ToID: id, Type: kgtypes.EdgeRelatesTo})
			continue
		}
		// (b) Foreign hit → upsert a knowledge proxy; the proxy id is a
		// post-create code link. A nil ex (no Execute seam) or a proxy-build
		// failure falls through to the drop.
		if ex != nil {
			if gt, name, node, found := crossgraph.LocateForeignNode(ctx, gc, graphs, id); found {
				if proxy, uerr := crossgraph.UpsertForeignProxy(ctx, ex, "knowledge", gt, name, id, node); uerr == nil && proxy != nil && proxy.Id != "" {
					cl.codeLinks = append(cl.codeLinks, proxy.Id)
					continue
				}
			}
		}
		// (c) Resolves nowhere (or proxy build failed) → drop+warn.
		cl.warnings = append(cl.warnings, fmt.Sprintf("link id %q not found in any indexed graph — the relates-to link was dropped (the node was still created)", id))
	}
}

// applyCodeLinks fires a post-create LinkOne (node--relates-to-->proxy) for each
// resolved foreign-proxy id. Every link is LOG-AND-CONTINUE: a LinkOne error
// appends a warning and moves on — the create has already succeeded and a failed
// decorative link must never turn the result into an error. Returns the
// accumulated warnings (caller folds them into its warning section).
func applyCodeLinks(ctx context.Context, gc GraphCaller, nodeID string, codeLinks []string) []string {
	var warnings []string
	for _, proxyID := range codeLinks {
		if proxyID == "" {
			continue
		}
		if lerr := LinkOne(ctx, gc, nodeID, proxyID, kgtypes.EdgeRelatesTo); lerr != nil {
			warnings = append(warnings, fmt.Sprintf("relates-to link to %q failed (%v) — skipped (the node was still created)", proxyID, lerr))
		}
	}
	return warnings
}
