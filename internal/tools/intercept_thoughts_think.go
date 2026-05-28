// SPDX-License-Identifier: Apache-2.0

// intercept_thoughts_think.go — FUL-247 / T-GTB6 client-side claim for
// thoughts(operation:think). The intercept LOWERS the think into a GENERIC
// create_batch MutationPlan via the Execute carrier seam (no dedicated server
// thought handler): it reproduces handleMutateCreateThought's invariants
// client-side — content validation, the thought node layout, session
// get-or-create + EdgeKGContains + EdgeNext lineage, EdgeBranchesFrom, and
// EdgeRelatesTo links with the 3-outcome cross-graph resolve. It does NOT write
// the ThoughtLatestTSKey watermark (CEO-dropped: write-only dead, no reader —
// finding 8e75d2c2) and adds no graph-meta-set primitive.

package tools

import (
	"context"
	"encoding/json"
	"fmt"
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
}

// composeThoughtCreate is the REUSABLE thought-create composition shared by the
// thoughts(think) intercept and the promote-metadata narrative. It reproduces
// the server handleMutateCreateThought invariants client-side: 3-outcome
// cross-graph link resolve, NodeThoughtSession get-or-create + EdgeNext-from-prev
// lineage, the generic create_batch (thought NodeBody + EdgeBranchesFrom +
// EdgeRelatesTo), the EdgeKGContains/EdgeNext session lineage, and the optional
// by-id status chase-up. Returns the created thought's ID.
//
// PERF: faithful session reproduction needs a get-or-create-session READ (one
// Match over NodeThoughtSession) + a session-thoughts READ (one EdgeKGContains
// traverse) BEFORE the create so the EdgeNext lineage edge points from the prior
// thought. These are extra round-trips vs the old single server Call — explicitly
// accepted; the create itself is ONE Execute (PersistBatch), the session edges
// are bounded LinkOne calls, and the status chase-up is at most one more Execute.
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
	var edges []kgwire.BatchEdge
	if a.BranchesFrom != "" {
		edges = append(edges, kgwire.BatchEdge{FromIdx: 0, ToIdx: -1, ToID: a.BranchesFrom, Type: kgtypes.EdgeBranchesFrom})
	}
	for _, linkID := range resolvedLinks {
		if linkID == "" {
			continue
		}
		edges = append(edges, kgwire.BatchEdge{FromIdx: 0, ToIdx: -1, ToID: linkID, Type: kgtypes.EdgeRelatesTo})
	}

	ids, err := PersistBatch(ctx, gc, []*knowledgev1.Node{&thoughtNode}, edges, "")
	if err != nil {
		return "", err
	}
	if len(ids) == 0 {
		return "", fmt.Errorf("create returned no id")
	}
	id := ids[0]

	// Session lineage edges (need the new thought ID): session→thought
	// (EdgeKGContains) + prev→thought (EdgeNext) when a prior thought exists.
	if lerr := linkSessionLineage(ctx, gc, sessionID, prevThoughtID, id); lerr != "" {
		return "", fmt.Errorf("%s", lerr)
	}

	// Caller requested a non-default status — chase up with a by-id UPDATE.
	if uerr := chaseThinkStatus(ctx, gc, id, a.Status); uerr != "" {
		return "", fmt.Errorf("%s", uerr)
	}

	return id, nil
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

// linkSessionLineage wires the session lineage edges AFTER the thought create
// (both need the new thought ID): EdgeKGContains session→thought, and EdgeNext
// prev→thought when the session held a prior thought. A no-op when sessionID is
// empty (the no-session case). Returns "" on success, else the error message.
func linkSessionLineage(ctx context.Context, gc GraphCaller, sessionID, prevThoughtID, thoughtID string) string {
	if sessionID == "" {
		return ""
	}
	if lerr := LinkOne(ctx, gc, sessionID, thoughtID, kgtypes.EdgeKGContains); lerr != nil {
		return "think: link session: " + lerr.Error()
	}
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

// getOrCreateThoughtSessionClient mirrors getOrCreateThoughtSession
// (tools_mutate_create_thought.go:128): Match every NodeThoughtSession, return
// the one whose SymbolName == name, else create a new session node and return
// its id. The Match rides the Execute carrier (engine.DecodeNodes).
func getOrCreateThoughtSessionClient(ctx context.Context, gc GraphCaller, name string) (string, error) {
	args, err := json.Marshal(map[string]any{"type": string(kgtypes.NodeThoughtSession), "limit": 0})
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
	for _, n := range sessions {
		if n.SymbolName == name {
			return n.Id, nil
		}
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
