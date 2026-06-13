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
	"sort"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/crossgraph"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
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
// (empty => "main"); when it resolves to a seeded agent node, the EdgeProduced
// hub edge also rides the batch. Code referents mechanically extracted from
// summary+content are resolve-or-dropped and born-linked on the same batch.
// Returns the created thought's ID.
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
func composeThoughtCreate(ctx context.Context, gc GraphCaller, a composeThoughtArgs) (string, error) {
	source := a.Source
	if source == "" {
		source = "llm:claude"
	}

	// Resolve cross-graph links (3-outcome mirror of ResolveOrProxy). Enumerate
	// the foreign graph list once, reuse across all links.
	resolvedLinks := resolveThinkLinks(ctx, gc, a.Links)

	// Session get-or-create + previous-thought read BEFORE the create, so the
	// EdgeNext lineage edge can point from the prior session thought.
	sessionID := ""
	var prevThoughtID string
	if a.Session != "" {
		sid, serr := getOrCreateThoughtSessionClient(ctx, gc, a.Session)
		if serr != nil {
			return "", serr
		}
		sessionID = sid
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
	var edges []kgwire.BatchEdge
	// Session containment rides the SAME create_batch as the thought node (slot 0)
	// — session--contains-->thought as an existing-node FROM endpoint (sessionID
	// resolved pre-batch above). This makes the contains edge ATOMIC with the
	// thought create: a created thought-with-session can never exist without its
	// containment edge (the live orphan-leak bug this fixes). Mirrors the
	// handleChargeClient idiom (thought_parent--charged_by-->charge on slot 0).
	if sessionID != "" {
		edges = append(edges, kgwire.BatchEdge{FromIdx: -1, FromID: sessionID, ToIdx: 0, Type: kgtypes.EdgeKGContains})
	}
	if a.BranchesFrom != "" {
		edges = append(edges, kgwire.BatchEdge{FromIdx: 0, ToIdx: -1, ToID: a.BranchesFrom, Type: kgtypes.EdgeBranchesFrom})
	}
	for _, linkID := range resolvedLinks {
		if linkID == "" {
			continue
		}
		edges = append(edges, kgwire.BatchEdge{FromIdx: 0, ToIdx: -1, ToID: linkID, Type: kgtypes.EdgeRelatesTo})
	}
	// Born-link arm: extracted code referents resolve-or-drop to knowledge-graph
	// proxies, each riding a thought--relates-to-->proxy (Method="code-ref") edge on
	// the SAME create_batch (see bornLinkCodeEdges). Atomic; never blocking.
	edges = append(edges, bornLinkCodeEdges(ctx, gc, a.Summary, a.Content)...)

	// Ticket context: a resolvable ticket_id rides the SAME create_batch
	// as the branches_from/links edges (ticket--contains-->thought, slot 0). An
	// unresolvable ticket is dropped with a logged warning (buildContextLinks
	// pre-validation) — it must never abort the think create. Session containment
	// already rode the batch above and links keep resolveThinkLinks; only the
	// ticket arm is delegated to the helper.
	if a.Ticket != "" {
		cl := buildContextLinks(ctx, gc, a.Ticket, "", nil)
		edges = append(edges, cl.batchEdges...)
		for _, w := range cl.warnings {
			slog.Warn("think: context link dropped", "detail", w)
		}
	}

	// Origin hub edge: when the origin resolves to a seeded agent node, an
	// agent--produced-->thought edge rides the SAME create_batch (see
	// originHubEdges). An unresolvable origin writes no edge — never blocking.
	edges = append(edges, originHubEdges(ctx, gc, a.Origin, originVal)...)

	ids, err := PersistBatch(ctx, gc, []*knowledgev1.Node{&thoughtNode}, edges, "")
	if err != nil {
		return "", err
	}
	if len(ids) == 0 {
		return "", fmt.Errorf("create returned no id")
	}
	id := ids[0]

	// Session EdgeNext lineage (needs the new thought ID): prev→thought when the
	// session held a prior thought. The session→thought EdgeKGContains edge already
	// rode the create_batch above (atomic); EdgeNext stays a post-create link
	// because its source (prevThoughtID) is the PRIOR session thought, not slot 0,
	// and a failed EdgeNext is a benign lineage gap (containment is intact).
	if lerr := linkSessionLineage(ctx, gc, prevThoughtID, id); lerr != "" {
		return "", fmt.Errorf("%s", lerr)
	}

	// Caller requested a non-default status — chase up with a by-id UPDATE.
	if uerr := chaseThinkStatus(ctx, gc, id, a.Status); uerr != "" {
		return "", fmt.Errorf("%s", uerr)
	}

	return id, nil
}

// originHubEdges resolves the developer-origin role to a seeded agent node and,
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
// only the thoughts(think) parse + content validation + render tail; the
// node/edge/session composition lives in composeThoughtCreate.
func handleThinkClient(ctx context.Context, deps ClientDeps, params kgtools.CallToolParams) kgtools.ToolResult {
	gc := deps.GraphCaller()
	if gc == nil {
		return errorResult("think: graph caller unavailable")
	}

	var a thinkArgs
	if err := json.Unmarshal(params.Arguments, &a); err != nil {
		return errorResult("invalid arguments: " + err.Error())
	}
	if strings.TrimSpace(a.Content) == "" {
		return errorResult("thoughts(think): content is required and must be non-empty (the hypothesis / observation / plan being recorded)")
	}
	// Summary is REQUIRED for think — the author-supplied search-optimized one-line
	// that makes the thought findable via recall. Reject missing/empty/whitespace
	// (and over-length) client-side BEFORE the wire, mirroring the content gate
	// above. validate.Summary enforces the same non-empty + SummaryMaxLen rule the
	// server applies to embed-only nodes, with the "summary is required" phrasing.
	if err := validate.Summary("thoughts(think)", "summary", a.Summary); err != nil {
		return errorResult(err.Error())
	}

	id, err := composeThoughtCreate(ctx, gc, composeThoughtArgs{
		Content:      a.Content,
		Summary:      strings.TrimSpace(a.Summary),
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

	return textResult(renderThinkTail(id, a))
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

// renderThinkTail builds the "Thought recorded → ID: ..." render + optional
// Session / Branches-from lines, matching the server tail verbatim.
func renderThinkTail(id string, a thinkArgs) string {
	sb := fmt.Sprintf("Thought recorded → ID: %s", id)
	if a.Session != "" {
		sb += fmt.Sprintf("\nSession: %s", a.Session)
	}
	if a.BranchesFrom != "" {
		sb += fmt.Sprintf("\nBranches from: %s", a.BranchesFrom)
	}
	return sb
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

// resolveThinkLinks resolves each link id via the shared 3-outcome
// resolveCrossGraphID (knowledge→raw, foreign→proxy, no-hit→raw as-is),
// enumerating the foreign graph list once.
func resolveThinkLinks(ctx context.Context, gc GraphCaller, links []string) []string {
	if len(links) == 0 {
		return nil
	}
	ex, _ := persistExecutor(gc)
	var graphs []crossgraph.ForeignGraph
	if ex != nil {
		graphs, _ = crossgraph.ListForeignGraphs(ctx, ex)
	}
	out := make([]string, 0, len(links))
	for _, l := range links {
		out = append(out, resolveCrossGraphID(ctx, gc, ex, graphs, l))
	}
	return out
}

// getOrCreateThoughtSessionClient resolves a session by name, store-portably:
// it issues a bounded symbol_name-EQ field-predicate browse over
// NodeThoughtSession (Selection.field_predicates, the server applies the WHERE
// where supported), then ALWAYS filters the returned rows to SymbolName==name
// client-side — defense-in-depth that stays correct even against an old or
// predicate-blind server that ignores field_predicates, so the resolver never
// attaches to a wrong-named session. On a same-name collision it returns the
// lowest id (sort.Strings, ids[0]) over the filtered set — the lowest-id tie-break
// idiom reproducing the deleted resolveSessionsByName backfill — else it creates a
// new session node and returns its id. The browse sets no explicit limit, so it
// inherits browseDefaultLimit=10; the store orders matches by id, keeping the
// lowest id on page one, so the tie-break is deterministic without a drain. The
// browse rides the Execute carrier (engine.DecodeNodes). The prior limit:0 browse
// — capped to browseDefaultLimit=10, so the existing session fell off the page
// once the corpus exceeded 10 sessions — was the duplicate-spawning defect this
// resolve replaces.
func getOrCreateThoughtSessionClient(ctx context.Context, gc GraphCaller, name string) (string, error) {
	args, err := json.Marshal(map[string]any{
		"type": string(kgtypes.NodeThoughtSession),
		"field_predicates": []map[string]string{
			{"field": "symbol_name", "op": "eq", "value": name},
		},
	})
	if err != nil {
		return "", fmt.Errorf("marshal session browse: %w", err)
	}
	resp, err := executeQuery(ctx, gc, args)
	if err != nil {
		return "", fmt.Errorf("session browse: %w", err)
	}
	sessions, derr := engine.DecodeNodes(resp)
	if derr != nil {
		return "", fmt.Errorf("session decode: %w", derr)
	}
	// Client-side SymbolName guard (DEFENSE-IN-DEPTH, store-portable — do NOT
	// delete): against a server that HONORS field_predicates (the OSS embedded
	// executor via nodeMatchesField, or the cloud executor's fieldPredicateClauses)
	// the browse already returns only exact-name matches and this filter is a cheap
	// no-op over 1-2 rows; against a predicate-BLIND server that ignored
	// field_predicates and returned an arbitrary capped page, it is the load-bearing
	// guard that prevents attaching to a session of the WRONG name. Collect every
	// match id, sort, and return the lowest (the established lowest-id tie-break) so
	// duplicate same-name sessions converge deterministically.
	var matchIDs []string
	for _, n := range sessions {
		if n.SymbolName == name {
			matchIDs = append(matchIDs, n.Id)
		}
	}
	if len(matchIDs) > 0 {
		sort.Strings(matchIDs)
		return matchIDs[0], nil
	}
	// Create a new session node. NodeThoughtSession is !Summarizable
	// (neverSummarize, node_type_eligibility.go) and is NOT in the
	// thought/charge summary-validation carve-out (engine_mutate_validate.go:69),
	// so it MUST carry a non-empty Summary or the server rejects the create with
	// "summary is required". The session has no author-supplied summary (only the
	// THOUGHT does); derive a composer summary from the session name — concise,
	// search-optimized, and well within SummaryMaxLen for any reasonable name.
	sessionNode := knowledgev1.Node{
		Type:       string(kgtypes.NodeThoughtSession),
		Source:     "llm:claude",
		SymbolName: name,
		Summary:    thoughtSessionSummary(name),
	}
	ids, perr := PersistBatch(ctx, gc, []*knowledgev1.Node{&sessionNode}, nil, "")
	if perr != nil {
		return "", fmt.Errorf("create session: %w", perr)
	}
	if len(ids) == 0 {
		return "", fmt.Errorf("create session: no id returned")
	}
	return ids[0], nil
}

// thoughtSessionSummary derives the composer-supplied, search-optimized summary
// for an auto-created NodeThoughtSession from its name. The session has no
// author-supplied summary (only the thought carries the new required param), so
// the composer mints one. truncateAtWordCreate bounds it well under
// validate.SummaryMaxLen even for a pathologically long session name.
func thoughtSessionSummary(name string) string {
	return truncateAtWordCreate("Reasoning session: "+name, validate.SummaryMaxLen)
}

// lastSessionThoughtID returns the ID of the most-recently-created thought
// already in the session (the "prev" the EdgeNext lineage edge originates from),
// or "" when the session has no prior thoughts. Mirrors getSessionThoughts +
// the prev = thoughts[len-2] selection (tools_mutate_create_thought.go:115-119):
// the new thought is NOT yet linked, so the current last session thought IS the
// prev. Reads the session's contained thoughts via an EdgeKGContains traverse
// and picks the latest by CreatedAt.
func lastSessionThoughtID(ctx context.Context, gc GraphCaller, sessionID string) string {
	nodes, err := TraverseDescendants(ctx, gc, sessionID, kgtypes.EdgeKGContains, 1)
	if err != nil || len(nodes) == 0 {
		return ""
	}
	var latest *knowledgev1.Node
	for _, n := range nodes {
		if kgtypes.NodeType(n.Type) != kgtypes.NodeThought {
			continue
		}
		if latest == nil || n.CreatedAt > latest.CreatedAt {
			latest = n
		}
	}
	if latest == nil {
		return ""
	}
	return latest.Id
}
