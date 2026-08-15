// SPDX-License-Identifier: Apache-2.0

// intercept_thoughts_context.go — client-side composer for
// thoughts(operation:recall, mode:"context"). Composes FIVE existing
// client-side read primitives into one bounded, deterministically-ordered
// context pack for session-start (brainstorm/research/retro):
//
//	(1) SEED   cross-type semantic search (SegmentManager().Search + hydrateEngineHits)
//	(2) EXPAND bounded edge expansion over the seed set (thought.FetchEdgesForNodeSet + FetchNodesByIDs)
//	(3) CHARGE per-thought charge state → validated/contested (thought.FetchChargesFor)
//	(4) RECENT UpdatedAt half-life overlay over seed+expand (applyTemporalRerank on a copy)
//	(5) TICKETS bounded type:ticket browse, terminal-status-excluded (direct QueryPlan+Execute)
//
// No new retrieval machinery — orchestration only over verified primitives.
// Each section is a bulk wire read (no N+1); sections are serial by
// necessity (expand depends on seed IDs, charge on the combined set).

package tools

import (
	"context"
	"sort"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"

	clientthought "github.com/fulminate-io/knowledge-mcp/internal/thought"
)

// Per-section size caps. Bound the pack so a session-start call stays inside one
// tool turn: only these capped per-section slices are ever rendered.
const (
	contextSeedCap         = 12  // cross-type semantic seed rows
	contextExpandCap       = 16  // edge-expanded peer rows
	contextRecentCap       = 8   // recency overlay rows
	contextTicketBrowseCap = 200 // raw type:ticket browse limit (pre-filter)
	contextTicketCap       = 8   // open-ticket rows after terminal-status filter
)

// contextTerminalStatuses is the terminal (closed/done) status set excluded from
// the open-tickets section. Status is an open string (team-workflow names), so
// the section EXCLUDES this terminal set rather than enumerating "open" — an
// unknown custom workflow state defaults to VISIBLE. Anchored on
// kgtypes.StatusCompleted; covers the graph-generic vocabulary plus the common
// Linear (Done/Cancelled) and closed/archived variants. Matched case-insensitively.
var contextTerminalStatuses = map[string]bool{
	"completed": true,
	"done":      true,
	"cancelled": true,
	"closed":    true,
	"archived":  true,
}

// contextExpandEdgeTypes are the KNOWLEDGE-graph edges walked one hop out from
// the seed set. Named constants (not raw strings) so the lowercase wire literals
// are correct by construction: EdgeKGContains="contains" (plan→phase, NOT the
// code-graph "CONTAINS"), EdgeInformedBy="informed-by", etc.
var contextExpandEdgeTypes = []kgtypes.EdgeType{
	kgtypes.EdgeInformedBy,
	kgtypes.EdgeRelatesTo,
	kgtypes.EdgeKGContains,
	kgtypes.EdgeDependsOn,
	kgtypes.EdgeAnswers,
}

// contextSection is one rendered band of the pack (a header label + its capped,
// deterministically-ordered rows).
type contextSection struct {
	key  string                // json key + text-header anchor (seeds/related/recent/tickets)
	rows []engine.SearchResult // capped, score-desc-then-id-asc ordered
}

// chargeAnnotations maps a thought node ID to its derived charge-state label
// (validated / contested / "") for inline rendering on thought rows.
type chargeAnnotations map[string]string

// handleRecallContext is the mode:context composer. It assembles the five
// sections in order — each delegating to a verified primitive — then renders a
// bounded, sectioned pack. gc nil-guard first (mirrors handleRecallClient).
func handleRecallContext(ctx context.Context, deps ClientDeps, a recallClientArgs) kgtools.ToolResult {
	gc := deps.GraphCaller()
	if gc == nil {
		return errorResult("recall context: graph client unavailable")
	}

	// (1) SEED — cross-type semantic search over the whole knowledge graph (NOT
	// thought-filtered: that is the point of the context pack).
	seeds, seedDegraded := composeContextSeed(ctx, deps, gc, a.Query)
	sortContextRows(seeds)
	if len(seeds) > contextSeedCap {
		seeds = seeds[:contextSeedCap]
	}

	// Track every node ID already placed so later sections dedup against it.
	seen := map[string]bool{}
	seedIDs := make([]string, 0, len(seeds))
	for i := range seeds {
		id := seeds[i].Node.GetId()
		seen[id] = true
		seedIDs = append(seedIDs, id)
	}

	// (2) EXPAND — depth-1 edge expansion over the seed SET (equals depth-2 from
	// the original query text, since the seed is already one hop from the query).
	expand := composeContextExpand(ctx, gc, seedIDs, seen)
	sortContextRows(expand)

	// (3) CHARGE state — collect thought-typed node IDs across seed+expand and
	// derive a validated/contested label from charge polarity counts. A bulk
	// edge read + ONE bulk hydrate inside FetchChargesFor.
	annotations := chargeAnnotations{}
	var thoughtIDs []string
	for _, r := range append(append([]engine.SearchResult{}, seeds...), expand...) {
		if r.Node.GetType() == string(kgtypes.NodeThought) {
			thoughtIDs = append(thoughtIDs, r.Node.GetId())
		}
	}
	if len(thoughtIDs) > 0 {
		chargesByThought := clientthought.FetchChargesFor(ctx, gc, thoughtIDs)
		for tid, charges := range chargesByThought {
			annotations[tid] = deriveChargeLabel(charges)
		}
	}

	// (4) RECENT — UpdatedAt half-life overlay over a COPY of the combined
	// seed+expand set; take the top contextRecentCap as a distinct "recently
	// active" section. Does NOT mutate the seed/expand ordering.
	recentSrc := append(append([]engine.SearchResult{}, seeds...), expand...)
	recent := make([]engine.SearchResult, len(recentSrc))
	copy(recent, recentSrc)
	applyTemporalRerank(recent, recentTemporalHalfLifeDays)
	if len(recent) > contextRecentCap {
		recent = recent[:contextRecentCap]
	}

	// (5) TICKETS — bounded type:ticket browse, terminal-status excluded. Direct
	// QueryPlan+Execute+DecodeNodes (the same plan-build/execute/decode shape
	// composeRecentBrowse uses — not a call, as that returns a rendered result).
	tickets := composeOpenTickets(ctx, gc)

	sections := []contextSection{
		{key: "seeds", rows: seeds},
		{key: "related", rows: expand},
		{key: "recent", rows: recent},
		{key: "tickets", rows: tickets},
	}
	return renderContextPack(sections, annotations, seedDegraded, a.Format)
}

// composeContextSeed runs the cross-type semantic seed search and reports
// whether the seed is DEGRADED. The seed is degraded when retrieval could not
// run: nil SegmentManager, empty query, or a non-nil error from Search / hydrate.
// A genuine zero-result run is NOT degraded. The degraded marker is the
// load-bearing signal distinguishing "retrieval could not run" from "nothing
// relates" — the false-negative this op exists to eliminate.
func composeContextSeed(ctx context.Context, deps ClientDeps, gc GraphCaller, query string) (seeds []engine.SearchResult, degraded bool) {
	mgr := deps.SegmentManager()
	if mgr == nil || query == "" {
		return nil, true
	}
	var queryVec []byte
	if emb := deps.Embedder(); emb != nil {
		if vec, err := emb.EmbedBinary(ctx, query); err == nil && len(vec) > 0 {
			queryVec = vec
		}
	}
	hits, err := mgr.Search(ctx, kgtypes.GraphKnowledge, knowledgeDefaultName, query, queryVec, contextSeedCap)
	if err != nil {
		return nil, true
	}
	rows, herr := hydrateEngineHits(ctx, gc, hydrateSelector{Graph: string(kgtypes.GraphKnowledge)}, hits)
	if herr != nil {
		return nil, true
	}
	return rows, false
}

// composeContextExpand walks one hop out from the seed SET over the expand edge
// types in a bulk both-direction RETURN_MODE_EDGES read, collects peer IDs
// outside the seed set (deduped, capped), and hydrates them in one bulk read.
// It marks every hydrated peer in seen so later sections dedup against it.
func composeContextExpand(ctx context.Context, gc GraphCaller, seedIDs []string, seen map[string]bool) []engine.SearchResult {
	if len(seedIDs) == 0 {
		return nil
	}
	edges, err := clientthought.FetchEdgesForNodeSet(ctx, gc, seedIDs, contextExpandEdgeTypes)
	if err != nil {
		return nil
	}
	peerIDs := make([]string, 0, len(edges))
	peerSeen := map[string]bool{}
	for i := range edges {
		for _, pid := range []string{edges[i].GetFromId(), edges[i].GetToId()} {
			if pid == "" || seen[pid] || peerSeen[pid] {
				continue
			}
			peerSeen[pid] = true
			peerIDs = append(peerIDs, pid)
		}
	}
	if len(peerIDs) > contextExpandCap {
		peerIDs = peerIDs[:contextExpandCap]
	}
	byID := clientthought.FetchNodesByIDs(ctx, gc, peerIDs)
	expand := make([]engine.SearchResult, 0, len(peerIDs))
	for _, pid := range peerIDs {
		if n, ok := byID[pid]; ok {
			expand = append(expand, engine.SearchResult{Node: n, Score: 1.0})
			seen[pid] = true
		}
	}
	return expand
}

// composeOpenTickets runs the bounded type:ticket browse and filters the decoded
// nodes to NON-terminal status (excludes contextTerminalStatuses,
// case-insensitive), capped at contextTicketCap, deterministically ordered.
func composeOpenTickets(ctx context.Context, gc GraphCaller) []engine.SearchResult {
	plan := &knowledgev1.QueryPlan{
		Selection: &knowledgev1.Selection{NodeTypes: []string{string(kgtypes.NodeTicket)}},
		Limit:     contextTicketBrowseCap,
		SkipTotal: true, // the filter reads only the decoded nodes, never Total
	}
	resp, err := gc.Execute(ctx, &knowledgev1.ExecuteRequest{
		Plan: &knowledgev1.ExecuteRequest_Query{Query: plan},
	})
	if err != nil {
		return nil
	}
	nodes, derr := engine.DecodeNodes(resp)
	if derr != nil {
		return nil
	}
	out := make([]engine.SearchResult, 0, len(nodes))
	for _, n := range nodes {
		if contextTerminalStatuses[strings.ToLower(strings.TrimSpace(n.GetStatus()))] {
			continue // terminal (closed/done) — excluded.
		}
		out = append(out, engine.SearchResult{Node: n, Score: 1.0})
	}
	sortContextRows(out)
	if len(out) > contextTicketCap {
		out = out[:contextTicketCap]
	}
	return out
}

// deriveChargeLabel maps a thought's charge slice to a state label from polarity
// counts: "validated" when positive charges dominate, "contested" when both
// polarities are present, "" when there are no charges.
func deriveChargeLabel(charges []*knowledgev1.Node) string {
	var pos, neg int
	for _, c := range charges {
		switch kgtypes.Value(c, "polarity") {
		case "positive":
			pos++
		case "negative":
			neg++
		}
	}
	switch {
	case pos > 0 && neg > 0:
		return "contested"
	case pos > 0:
		return "validated"
	case neg > 0:
		return "contested"
	default:
		return ""
	}
}

// sortContextRows imposes deterministic within-section ordering: score
// descending, then Id ascending as a stable tiebreak.
func sortContextRows(rows []engine.SearchResult) {
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Score != rows[j].Score {
			return rows[i].Score > rows[j].Score
		}
		return rows[i].Node.GetId() < rows[j].Node.GetId()
	})
}

// seedDegradedMarker is the explicit "retrieval could not run" line rendered in
// BOTH formats when the semantic seed is degraded — distinct from a non-degraded
// zero-row seed (which renders as a normal empty section with no marker). It is
// the false-negative-eliminating signal: "retrieval could not run" vs "nothing
// relates".
const seedDegradedMarker = "semantic seed unavailable — no query provided / search engine offline"

// contextRow is the per-row projection for the json pack — a deliberately small
// subset of engine.SearchJSONResult (render_search.go) carrying only what a
// session-start pack needs, plus an optional charge-state annotation on thoughts.
type contextRow struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	Type       string  `json:"type"`
	Score      float64 `json:"score"`
	SymbolName string  `json:"symbol_name,omitempty"`
	Summary    string  `json:"summary,omitempty"`
	Status     string  `json:"status,omitempty"`
	Charge     string  `json:"charge,omitempty"`
}

// projectContextRow maps an engine.SearchResult to the contextRow shape, mirroring
// the engine.SearchJSONResult field set (render_search.go:153-171 name-fallback
// included) and folding in the derived charge label for thought rows.
func projectContextRow(r engine.SearchResult, ann chargeAnnotations) contextRow {
	name := r.Node.GetSymbolName()
	if name == "" {
		name = r.Node.GetDescription()
	}
	return contextRow{
		ID:         r.Node.GetId(),
		Name:       name,
		Type:       r.Node.GetType(),
		Score:      r.Score,
		SymbolName: r.Node.GetSymbolName(),
		Summary:    r.Node.GetSummary(),
		Status:     r.Node.GetStatus(),
		Charge:     ann[r.Node.GetId()],
	}
}

// renderContextPack emits the bounded, sectioned context pack in the caller's
// format. It does NOT delegate to engine.RenderForCaller (a flat single-envelope
// {query,total,results} renderer that cannot emit a sectioned pack) nor to the
// thought-only FormatRecallResults; it orchestrates the per-row projection over
// the sections directly.
func renderContextPack(sections []contextSection, ann chargeAnnotations, seedDegraded bool, format string) kgtools.ToolResult {
	if format == "json" {
		return renderContextPackJSON(sections, ann, seedDegraded)
	}
	return renderContextPackText(sections, ann, seedDegraded)
}

// renderContextPackJSON assembles the {seeds,related,recent,tickets,charges}
// pack via the per-row projection and marshals it with jsonResult. The seeds
// section carries an explicit seed_degraded flag + marker so the degraded path
// is machine-distinguishable from a zero-row seed.
func renderContextPackJSON(sections []contextSection, ann chargeAnnotations, seedDegraded bool) kgtools.ToolResult {
	pack := map[string]any{}
	charges := map[string]string{}
	for _, sec := range sections {
		rows := make([]contextRow, 0, len(sec.rows))
		for _, r := range sec.rows {
			cr := projectContextRow(r, ann)
			rows = append(rows, cr)
			if cr.Charge != "" {
				charges[cr.ID] = cr.Charge
			}
		}
		pack[sec.key] = rows
	}
	// charges section: the per-thought derived charge-state map across all rows.
	pack["charges"] = charges
	if seedDegraded {
		pack["seed_degraded"] = true
		pack["seed_marker"] = seedDegradedMarker
	}
	return jsonResult(pack)
}

// renderContextPackText writes a compact markdown pack: one header per section
// and one line per row (SymbolName/name + type + Id + score), with the
// validated/contested charge label annotated inline on thought rows. The seeds
// section renders the explicit degraded marker line when seedDegraded.
func renderContextPackText(sections []contextSection, ann chargeAnnotations, seedDegraded bool) kgtools.ToolResult {
	headers := map[string]string{
		"seeds":   "Seed",
		"related": "Related (edge-expanded)",
		"recent":  "Recently active",
		"tickets": "Open tickets",
	}
	var b strings.Builder
	b.WriteString("# Context pack\n")
	for _, sec := range sections {
		b.WriteString("\n## ")
		b.WriteString(headers[sec.key])
		b.WriteString("\n")
		if sec.key == "seeds" && seedDegraded {
			b.WriteString("- ")
			b.WriteString(seedDegradedMarker)
			b.WriteString("\n")
			continue
		}
		if len(sec.rows) == 0 {
			b.WriteString("- (none)\n")
			continue
		}
		for _, r := range sec.rows {
			b.WriteString(contextTextLine(r, ann))
		}
	}
	return textResult(b.String())
}

// contextTextLine renders one markdown bullet for a row, appending the charge
// label for an annotated thought.
func contextTextLine(r engine.SearchResult, ann chargeAnnotations) string {
	name := r.Node.GetSymbolName()
	if name == "" {
		name = r.Node.GetDescription()
	}
	var b strings.Builder
	b.WriteString("- ")
	b.WriteString(name)
	b.WriteString(" [")
	b.WriteString(r.Node.GetType())
	b.WriteString("] ")
	b.WriteString(r.Node.GetId())
	if label := ann[r.Node.GetId()]; label != "" {
		b.WriteString(" (")
		b.WriteString(label)
		b.WriteString(")")
	}
	b.WriteString("\n")
	return b.String()
}
