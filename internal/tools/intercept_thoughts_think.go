// SPDX-License-Identifier: Apache-2.0

// intercept_thoughts_think.go — client-side claim for
// thoughts(operation:think). The intercept LOWERS the think into a GENERIC
// create_batch MutationPlan via the Execute carrier seam (no dedicated server
// thought handler): it reproduces handleMutateCreateThought's invariants
// client-side — content validation, the thought node layout, session
// get-or-create with the EdgeKGContains containment edge riding the create_batch
// ATOMICALLY (only the EdgeNext lineage link is post-create), EdgeBranchesFrom,
// and EdgeRelatesTo links with the 3-outcome cross-graph resolve. It does NOT write
// the ThoughtLatestTSKey watermark (intentionally dropped: write-only dead,
// no reader) and adds no graph-meta-set primitive.

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/crossgraph"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
	"github.com/fulminate-io/knowledge-mcp/internal/validate"
)

// thinkArgs is the parsed thoughts(operation:think) shape. Mirrors the
// server-side handleThink struct at cmd/knowledge-server/tools/
// tools_thought.go:134-140.
type thinkArgs struct {
	Content      string   `json:"content"`
	Summary      string   `json:"summary"`
	Session      string   `json:"session"`
	BranchesFrom string   `json:"branches_from"`
	Links        []string `json:"links"`
	Status       string   `json:"status"`
	// TicketID: optional active-ticket pass-through so a thought is
	// born linked to the work item that produced it (ticket--contains-->thought).
	// Pre-validated + drop-tolerant via buildContextLinks; an unresolvable
	// ticket_id is dropped with a warning, never blocking the think create.
	TicketID string `json:"ticket_id"`
	// Origin: optional developer-origin role (planner|implementer|reviewer|
	// researcher|tester|orchestrator|main; absent => main). Open string, never
	// blocking: stamped as origin metadata, and when it resolves to a seeded agent
	// node, an agent--produced-->thought hub edge rides the create_batch. The raw
	// param flows through to composeThoughtCreate; default-to-main normalization
	// happens in the resolver, not here.
	Origin string `json:"origin"`

	// Negation-gate proof-of-work fields — read ONLY by InterceptNegationGate
	// (negation_gate.go), which runs BEFORE this think handler in the intercept
	// chain. For a thoughts(think) supersession (branches_from set) — including the
	// branches_from + status:invalidated shape — VerifiedQuote must be a verbatim
	// substring of the superseded (branches_from) node's CURRENT source; CitedRange
	// is the optional file:line-range locality hint. They are GATE-only inputs: the
	// think write IGNORES them (proof-of-work, never persisted), so they are not
	// threaded into composeThoughtCreate.
	VerifiedQuote string `json:"verified_quote,omitempty"`
	CitedRange    string `json:"cited_range,omitempty"`
}

// composeThoughtArgs is the reusable input to composeThoughtCreate. It is the
// thinkArgs payload plus an explicit Source so non-think callers (the
// promote-metadata narrative) can compose a thought with the same invariants
// without going through the thoughts(think) parse/render path. Source defaults
// to "llm:claude" when empty.
type composeThoughtArgs struct {
	Content      string
	Summary      string
	Source       string
	Session      string
	BranchesFrom string
	Links        []string
	Status       string
	// Ticket: optional active-ticket ID. When set + resolvable, a
	// ticket--contains-->thought edge rides the same create_batch as the
	// branches_from/links edges; an unresolvable ticket is dropped with a
	// warning (buildContextLinks pre-validation). Zero value = no ticket link,
	// so the promote-metadata caller is unaffected.
	Ticket string
	// Origin: optional developer-origin role of the agent recording this thought
	// (planner|implementer|reviewer|researcher|tester|orchestrator|main; absent =>
	// main). Stamped as origin metadata and, when resolvable to a seeded agent
	// node, rides an agent--produced-->thought hub edge on the create_batch. Zero
	// value = main metadata, no hub edge, so the promote-metadata caller is
	// unaffected.
	Origin string
}

// composeThoughtCreate is the REUSABLE thought-create composition shared by the
// thoughts(think) intercept and the promote-metadata narrative. It reproduces
// the server handleMutateCreateThought invariants client-side: 3-outcome
// cross-graph link resolve, NodeThoughtSession get-or-create with the
// EdgeKGContains session→thought edge riding the generic create_batch (thought
// NodeBody + EdgeKGContains + EdgeBranchesFrom + EdgeRelatesTo + the born-link
// thought--relates-to-->code-proxy edges (Method="code-ref") + the optional
// agent--produced-->thought origin hub edge) ATOMICALLY, the post-create
// EdgeNext-from-prev session lineage link, and the optional by-id status
// chase-up. The developer-origin role is ALWAYS stamped as `origin` metadata
// (empty => "main"); when it resolves to a user-authored agent node, the EdgeProduced
// hub edge also rides the batch. Code referents mechanically extracted from
// summary+content are resolve-or-dropped and born-linked on the same batch.
//
// Returns a thinkReceipt of what LANDED — the new thought's ID plus the session
// node it resolved to, whether the ticket contains edge rode the batch, the
// resolved/unresolved link split, and the born-link count. The outcomes are
// collected here because this is the only place that holds them; the render tail
// reports them instead of echoing the caller's own arguments back (see
// intercept_thoughts_think_receipt.go for why that distinction is load-bearing).
//
// PERF: faithful session reproduction needs a get-or-create-session READ + a
// session-thoughts READ (one EdgeKGContains traverse) BEFORE the create so the
// EdgeNext lineage edge points from the prior thought. The session READ is ONE
// bounded symbol_name-EQ field-predicate browse (Selection.field_predicates),
// which the server filters where supported (the embedded executor via
// nodeMatchesField; the cloud Postgres executor via fieldPredicateClauses) — a
// single wire read whose cost does NOT grow with the session count — followed by
// an always-on client-side SymbolName==name guard, with the lowest-id node chosen
// on a same-name collision. These are extra round-trips vs the old single server
// Call — explicitly accepted; the create itself is ONE Execute (PersistBatch)
// carrying the session containment edge, the EdgeNext lineage is a single bounded
// LinkOne, and the status chase-up is at most one more Execute.
func composeThoughtCreate(ctx context.Context, gc GraphCaller, a composeThoughtArgs) (thinkReceipt, error) {
	source := a.Source
	if source == "" {
		source = "llm:claude"
	}
	receipt := thinkReceipt{
		SessionName:  a.Session,
		TicketID:     a.Ticket,
		BranchesFrom: a.BranchesFrom,
	}

	// Resolve cross-graph links (3-outcome mirror of ResolveOrProxy). Enumerate
	// the foreign graph list once, reuse across all links.
	resolvedLinks, linkOutcome := resolveThinkLinks(ctx, gc, a.Links)
	receipt.LinksResolved, receipt.LinksUnresolved = linkOutcome.resolved, linkOutcome.unresolved

	// Session get-or-create + previous-thought read BEFORE the create, so the
	// EdgeNext lineage edge can point from the prior session thought.
	sessionID := ""
	var prevThoughtID string
	if a.Session != "" {
		sid, serr := getOrCreateThoughtSessionClient(ctx, gc, a.Session)
		if serr != nil {
			return thinkReceipt{}, serr
		}
		sessionID = sid
		receipt.SessionID = sid
		prevThoughtID = lastSessionThoughtID(ctx, gc, sessionID)
	}

	// Build the thought node + branches_from/links edges, lower onto create_batch.
	// Summary is the author-supplied, search-optimized one-liner — set it on the
	// node so AutoSummary (node_autosummary_compose.go:27) honors it verbatim
	// rather than falling back to the content-derived autoSummaryThought. The
	// think handler guarantees a non-empty summary client-side; the reusable
	// promote-metadata caller supplies its own.
	thoughtNode := knowledgev1.Node{
		Type:       string(kgtypes.NodeThought),
		Source:     source,
		SymbolName: truncateAtWordCreate(a.Content, 60),
		Content:    a.Content,
		Summary:    a.Summary,
		Status:     kgtypes.StatusHypothesized,
	}
	if a.Session != "" {
		kgtypes.SetValue(&thoughtNode, "session", a.Session)
	}
	// Origin (developer-origin role): ALWAYS stamp the normalized value as
	// metadata (empty => "main"), mirroring the session stamp above — this is the
	// cheap meta-filter facet and lands regardless of agent resolution.
	originVal := a.Origin
	if originVal == "" {
		originVal = "main"
	}
	kgtypes.SetValue(&thoughtNode, "origin", originVal)

	edges := composeThoughtEdges(ctx, gc, a, thoughtEdgeInputs{
		sessionID:     sessionID,
		originVal:     originVal,
		resolvedLinks: resolvedLinks,
	}, &receipt)

	ids, err := PersistBatch(ctx, gc, []*knowledgev1.Node{&thoughtNode}, edges, "")
	if err != nil {
		return thinkReceipt{}, err
	}
	if len(ids) == 0 {
		return thinkReceipt{}, fmt.Errorf("create returned no id")
	}
	id := ids[0]
	receipt.ID = id

	// Session EdgeNext lineage (needs the new thought ID): prev→thought when the
	// session held a prior thought. The session→thought EdgeKGContains edge already
	// rode the create_batch above (atomic); EdgeNext stays a post-create link
	// because its source (prevThoughtID) is the PRIOR session thought, not slot 0,
	// and a failed EdgeNext is a benign lineage gap (containment is intact).
	if lerr := linkSessionLineage(ctx, gc, prevThoughtID, id); lerr != "" {
		return thinkReceipt{}, fmt.Errorf("%s", lerr)
	}

	// Caller requested a non-default status — chase up with a by-id UPDATE.
	if uerr := chaseThinkStatus(ctx, gc, id, a.Status); uerr != "" {
		return thinkReceipt{}, fmt.Errorf("%s", uerr)
	}

	return receipt, nil
}

// thoughtEdgeInputs carries the values composeThoughtCreate resolved BEFORE the
// batch is assembled — the session node id (pre-resolved so its containment edge
// can ride the create atomically), the normalized origin, and the resolved link
// ids. Grouped into one struct so the edge assembly takes a readable parameter
// list rather than five positional strings and slices.
type thoughtEdgeInputs struct {
	sessionID     string
	originVal     string
	resolvedLinks []string
}

// composeThoughtEdges assembles EVERY batch edge the new thought (slot 0) rides
// with: session containment, branches-from, the caller's relates-to links, the
// born-link code referents, the ticket containment, and the origin hub edge. All
// of them ride the SAME create_batch as the node, which is what makes them atomic
// with it — a created thought can never exist without the edges assembled here.
//
// It also fills the outcome fields of receipt that are only knowable at assembly
// time (born-link count, ticket linked-or-dropped), which is why receipt is a
// pointer: the write receipt reports what the batch actually carried.
func composeThoughtEdges(ctx context.Context, gc GraphCaller, a composeThoughtArgs, in thoughtEdgeInputs, receipt *thinkReceipt) []kgwire.BatchEdge {
	var edges []kgwire.BatchEdge
	// Session containment rides the SAME create_batch as the thought node (slot 0)
	// — session--contains-->thought as an existing-node FROM endpoint (sessionID
	// resolved pre-batch by the caller). This makes the contains edge ATOMIC with
	// the thought create: a created thought-with-session can never exist without its
	// containment edge (the live orphan-leak bug this fixes). Mirrors the
	// handleChargeClient idiom (thought_parent--charged_by-->charge on slot 0).
	if in.sessionID != "" {
		edges = append(edges, kgwire.BatchEdge{FromIdx: -1, FromID: in.sessionID, ToIdx: 0, Type: kgtypes.EdgeKGContains})
	}
	if a.BranchesFrom != "" {
		edges = append(edges, kgwire.BatchEdge{FromIdx: 0, ToIdx: -1, ToID: a.BranchesFrom, Type: kgtypes.EdgeBranchesFrom})
	}
	for _, linkID := range in.resolvedLinks {
		if linkID == "" {
			continue
		}
		edges = append(edges, kgwire.BatchEdge{FromIdx: 0, ToIdx: -1, ToID: linkID, Type: kgtypes.EdgeRelatesTo})
	}
	// Born-link arm: extracted code referents resolve-or-drop to knowledge-graph
	// proxies, each riding a thought--relates-to-->proxy (Method="code-ref") edge on
	// the SAME create_batch (see bornLinkCodeEdges). Atomic; never blocking.
	bornLinks := bornLinkCodeEdges(ctx, gc, a.Summary, a.Content)
	receipt.BornLinks = len(bornLinks)
	edges = append(edges, bornLinks...)

	// Ticket context: a resolvable ticket_id rides the SAME create_batch
	// as the branches_from/links edges (ticket--contains-->thought, slot 0). An
	// unresolvable ticket is dropped with a logged warning (buildContextLinks
	// pre-validation) — it must never abort the think create. Session containment
	// already rode the batch above and links keep resolveThinkLinks; only the
	// ticket arm is delegated to the helper.
	if a.Ticket != "" {
		cl := buildContextLinks(ctx, gc, a.Ticket, "", nil)
		edges = append(edges, cl.batchEdges...)
		// A ticket edge on the batch IS the linked outcome: buildContextLinks
		// pre-validates the ticket and emits the contains edge only on a hit, so
		// an empty batchEdges here means the ticket was dropped.
		receipt.TicketLinked = len(cl.batchEdges) > 0
		for _, w := range cl.warnings {
			slog.Warn("think: context link dropped", "detail", w)
		}
	}

	// Origin hub edge: when the origin resolves to a user-authored agent node, an
	// agent--produced-->thought edge rides the SAME create_batch (see
	// originHubEdges). An unresolvable origin writes no edge — never blocking.
	return append(edges, originHubEdges(ctx, gc, a.Origin, in.originVal)...)
}

// originHubEdges resolves the developer-origin role to a user-authored agent node and,
// on a hit, returns the agent--produced-->thought hub edge to ride the SAME
// create_batch (atomic, the EXACT existing-node-From shape as the session-contains
// edge). An unresolvable origin (e.g. "main"/"orchestrator"/a custom value with no
// agent node) returns nil and degrades to metadata-only with a Debug log — never
// blocking. originVal is the already-normalized value (for the debug log only).
func originHubEdges(ctx context.Context, gc GraphCaller, origin, originVal string) []kgwire.BatchEdge {
	agentID := resolveOriginAgentID(ctx, gc, origin)
	if agentID == "" {
		slog.Debug("think: origin agent unresolved — metadata only", "origin", originVal)
		return nil
	}
	// Intentional EdgeProduced inversion: From=agent node, To=thought (the agent
	// produced the thought). edge_types.go documents the thought->artifact
	// direction; this agent-hub provenance edge reverses it on purpose — do not
	// "fix" the apparent backwards direction.
	return []kgwire.BatchEdge{{FromIdx: -1, FromID: agentID, ToIdx: 0, Type: kgtypes.EdgeProduced}}
}

// handleThinkClient claims thoughts(operation:think) and lowers it onto a
// generic create_batch MutationPlan via the reusable composeThoughtCreate
// composition (which reproduces handleMutateCreateThought). This handler owns
// only the thoughts(think) parse + content validation + the receipt render and
// its non-fatal warnings; the node/edge/session composition lives in
// composeThoughtCreate.
func handleThinkClient(ctx context.Context, deps ClientDeps, params kgtools.CallToolParams) kgtools.ToolResult {
	gc := deps.GraphCaller()
	if gc == nil {
		return errorResult("think: graph caller unavailable")
	}

	var a thinkArgs
	if err := json.Unmarshal(params.Arguments, &a); err != nil {
		return errorResult("invalid arguments: " + decodeArgsError(params.Arguments, err))
	}
	if strings.TrimSpace(a.Content) == "" {
		return errorResult("thoughts(think): content is required and must be non-empty (the hypothesis / observation / plan being recorded)")
	}
	// Summary is REQUIRED for think — the author-supplied search-optimized one-line
	// that makes the thought findable via recall. Missing/empty/whitespace is
	// rejected client-side BEFORE the wire, mirroring the content gate above. An
	// over-length summary is NOT rejected: validate.ClampSummary clamps it to
	// SummaryMaxLen at a word boundary and returns a non-fatal warning, so the
	// think still succeeds with a clamped (findable) summary.
	clamped, clampWarn, serr := validate.ClampSummary("thoughts(think)", "summary", a.Summary)
	if serr != nil {
		return errorResult(serr.Error())
	}
	a.Summary = clamped

	receipt, err := composeThoughtCreate(ctx, gc, composeThoughtArgs{
		Content: a.Content,
		// a.Summary is already trimmed + clamped by validate.ClampSummary.
		Summary:      a.Summary,
		Source:       "llm:claude",
		Session:      a.Session,
		BranchesFrom: a.BranchesFrom,
		Links:        a.Links,
		Status:       a.Status,
		Ticket:       a.TicketID,
		Origin:       a.Origin,
	})
	if err != nil {
		return errorResult("think: " + err.Error())
	}

	var sb strings.Builder
	sb.WriteString(renderThinkTail(receipt))
	// Non-fatal warnings, in one section beside the receipt: the summary clamp,
	// then the parameter-shaped-tail advisory. The advisory NEVER refuses — see
	// intercept_thoughts_think_receipt.go — so it rides a successful write,
	// alongside the receipt it defers to for what actually landed. The shape that
	// DOES refuse never reaches here: rejectSwallowedParamValues runs at
	// interceptThoughtsOp, above this handler and before any write.
	var warnings []string
	if clampWarn != "" {
		warnings = append(warnings, clampWarn)
	}
	if tailWarn := paramShapedTailWarning("content", a.Content); tailWarn != "" {
		warnings = append(warnings, tailWarn)
	}
	writeClientWarningsSection(&sb, warnings)
	return textResult(sb.String())
}

// chaseThinkStatus applies a non-default caller status via a by-id UPDATE over
// the Execute carrier (the server's status chase-up). A no-op when status is
// empty or the default hypothesized. Returns "" on success, else the error.
func chaseThinkStatus(ctx context.Context, gc GraphCaller, id, status string) string {
	if status == "" || status == kgtypes.StatusHypothesized {
		return ""
	}
	updArgs, merr := json.Marshal(map[string]any{"operation": "update", "id": id, "status": status})
	if merr != nil {
		return "think: marshal status update: " + merr.Error()
	}
	if _, uerr := executeMutate(ctx, gc, updArgs); uerr != nil {
		return "think: status update: " + uerr.Error()
	}
	return ""
}

// linkSessionLineage wires the EdgeNext lineage edge AFTER the thought create
// (it needs the new thought ID): EdgeNext prev→thought when the session held a
// prior thought. The session→thought EdgeKGContains edge is NOT linked here — it
// rides the create_batch atomically in composeThoughtCreate. A no-op when
// prevThoughtID is empty (no prior session thought). Returns "" on success, else
// the error message.
func linkSessionLineage(ctx context.Context, gc GraphCaller, prevThoughtID, thoughtID string) string {
	if prevThoughtID != "" {
		if lerr := LinkOne(ctx, gc, prevThoughtID, thoughtID, kgtypes.EdgeNext); lerr != nil {
			return "think: link lineage: " + lerr.Error()
		}
	}
	return ""
}

// linkResolveOutcome counts how the caller's link ids fared, for the write
// receipt. unresolved is NOT "dropped": a no-hit id still rides the batch as a
// raw relates-to target (server outcome c), so the edge is attempted — the count
// exists so a caller can see that an id it passed matched nothing indexed.
type linkResolveOutcome struct {
	resolved   int
	unresolved int
}

// resolveThinkLinks resolves each link id via the shared 3-outcome
// resolveCrossGraphIDOutcome (knowledge→resolved id, foreign→proxy, no-hit→raw
// as-is), enumerating the foreign graph list once, and reports the
// resolved/unresolved split alongside the resolved ids — the outcome-reporting
// form is used precisely because the split is invisible in the returned ids.
// Empty entries are neither resolved nor unresolved: the composer skips them
// when building edges.
func resolveThinkLinks(ctx context.Context, gc GraphCaller, links []string) ([]string, linkResolveOutcome) {
	var outcome linkResolveOutcome
	if len(links) == 0 {
		return nil, outcome
	}
	ex, _ := persistExecutor(gc)
	var graphs []crossgraph.ForeignGraph
	if ex != nil {
		graphs, _ = crossgraph.ListForeignGraphs(ctx, ex)
	}
	out := make([]string, 0, len(links))
	for _, l := range links {
		id, ok := resolveCrossGraphIDOutcome(ctx, gc, ex, graphs, l)
		out = append(out, id)
		switch {
		case l == "":
		case ok:
			outcome.resolved++
		default:
			outcome.unresolved++
		}
	}
	return out, outcome
}

// The session seam — getOrCreateThoughtSessionClient, thoughtSessionSummary and
// lastSessionThoughtID — lives in intercept_thoughts_think_session.go.
