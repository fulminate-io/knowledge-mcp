// SPDX-License-Identifier: Apache-2.0

// thought.go — client-side intercept for the full `thoughts` MCP tool
// surface (think, charge, recall, trace, propagate, similarity_report,
// adjacency, charges_for — all claimed client-side) and the `query` MCP tool's
// reflective + thought modes (personality, tensions, blind_spots,
// summary, clusters, evolution, influence, examine for NodeThought IDs,
// simulate, plus query() shapes carrying mode:timeline / mode:charges
// or any thought-property filter — valence_min/valence_max/
// magnitude_min/consistency_max/session/connected_to/status).
//
// Every thought-domain op is claimed client-side. The
// server-side handlers return the client-intercept-required sentinel
// (see cmd/knowledge-server/tools/tools_thought.go and tools_thought_query.go
// and tools_query_routes.go); InterceptThoughts is the only path that
// produces real results. Mirrors the shape of InterceptWorker — same
// name-filtering, same fall-through convention for non-thought paths
// like examine of generic nodes.

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"

	clientthought "github.com/fulminate-io/knowledge-mcp/internal/thought"
)

// thoughtsArgs is the parsed wire shape for thoughts(...) — only the
// fields InterceptThoughts needs to dispatch are decoded; everything
// else is left for the server-side handler when the call falls through.
type thoughtsArgs struct {
	Operation string `json:"operation"`
	Mode      string `json:"mode"`
	AllTypes  bool   `json:"all_types"`
	Format    string `json:"format"`
}

// queryReflectArgs is the parsed wire shape for query(mode:...) when the
// mode is one of the reflective surface. Only the dispatch-relevant
// fields are decoded.
//
// queryReflectArgs uses *flexFloat for the thought-property filters because
// the client-side intercept must accept both raw numbers and quoted-string
// numeric forms (some LLMs double-encode). The broader server-side
// queryArgs (cmd/knowledge-server/tools/tools_query_args.go:26-29) uses
// *float64 — it parses pre-routing wire shapes that don't currently need
// flex-typed decode.
type queryReflectArgs struct {
	Mode     string `json:"mode"`
	Cluster  string `json:"cluster"`
	ClusterA string `json:"cluster_a"`
	ClusterB string `json:"cluster_b"`
	Limit    int    `json:"limit"`
	Format   string `json:"format"`
	Graph    string `json:"graph"`
	// Granularity selects the reflect view for summary + personality:
	// "cluster" (default — current per-cluster behavior, where the trust scalars
	// are calibrated) or "topic" (roll up by topic membership, displaying topic
	// summaries as names). Empty == "cluster".
	Granularity string `json:"granularity"`
	// Sort selects the display ordering for the EVIDENCED section of
	// mode:influence. Selection is evidence-aware: charged thoughts are ranked by
	// influence×(1+chargeWeight) into the evidenced top-N, while zero-charge
	// structural hubs are returned in a separate labeled backfill section.
	// "influence" (default, empty) keeps the influence×(1+chargeWeight) selection
	// order; "composite" reorders the already-selected evidenced set by
	// influence×magnitude FOR DISPLAY. composite is a within-set display reorder
	// ONLY: it does NOT change which thoughts are selected and does not touch the
	// backfill section. A consumer must not read "composite" as widening the
	// candidate set.
	Sort string `json:"sort"`
	// Thought-domain routing fields. Without these the intercept cannot
	// distinguish query(mode:examine, id:<thought>) from
	// query(mode:examine, id:<file_symbol>), nor can it pick up the
	// timeline/charges/thought-property filter shapes that route to recall.
	ID           string     `json:"id"`
	Text         string     `json:"text"`
	Type         string     `json:"type"`
	Session      string     `json:"session"`
	ConnectedTo  string     `json:"connected_to"`
	Status       string     `json:"status"`
	ValenceMin   *flexFloat `json:"valence_min"`
	ValenceMax   *flexFloat `json:"valence_max"`
	MagnitudeMin *flexFloat `json:"magnitude_min"`
	ConsistMax   *flexFloat `json:"consistency_max"`
}

// InterceptThoughts dispatches the reflective-surface operations of the
// `thoughts` and `query` MCP tools into the client-side
// cmd/knowledge/internal/thought package. Returns (true, result) when
// the call was handled; (false, zero) otherwise (the host falls back to
// forwarding to the server-side handler).
//
// Routing:
//   - thoughts(propagate): handled
//   - thoughts(recall, mode:"clusters"[, all_types]): handled
//   - query(mode in {personality, influence, tensions, blind_spots,
//     summary, evolution, clusters}): handled
//   - everything else: fall through to server.
func InterceptThoughts(deps ClientDeps, params kgtools.CallToolParams) (bool, kgtools.ToolResult) {
	switch params.Name {
	case "thoughts":
		return interceptThoughtsOp(deps, params)
	case "query":
		return interceptQueryReflect(deps, params)
	}
	return false, kgtools.ToolResult{}
}

// interceptThoughtsOp dispatches the reflective subset of the `thoughts`
// tool. Unrecognized ops fall through unchanged.
func interceptThoughtsOp(deps ClientDeps, params kgtools.CallToolParams) (bool, kgtools.ToolResult) {
	var a thoughtsArgs
	if err := json.Unmarshal(params.Arguments, &a); err != nil {
		// Don't claim the call — let the server surface the parse
		// error so the message matches every other parse-error site.
		return false, kgtools.ToolResult{}
	}
	ctx := context.Background()
	switch a.Operation {
	case "think":
		return true, handleThinkClient(ctx, deps, params)
	case "charge":
		return true, handleChargeClient(ctx, deps, params)
	case "trace":
		return true, handleTraceClient(ctx, deps, params)
	case "recall":
		// handleRecallClient subsumes the cluster-mode special case
		// (mode:clusters early-returns to handleRecallClusters from
		// inside the handler — see intercept_thoughts_recall.go).
		return true, handleRecallClient(ctx, deps, params)
	case "propagate":
		return true, handlePropagateClient(ctx, deps, params)
	case "similarity_report":
		return true, handleSimilarityReportClient(ctx, deps, params)
	case "adjacency":
		return true, handleAdjacencyClient(ctx, deps, params)
	case "charges_for":
		return true, handleChargesForClient(ctx, deps, params)
	}
	return false, kgtools.ToolResult{}
}

// interceptQueryReflect dispatches the reflective subset of query(mode:...).
// Unrecognized modes fall through.
func interceptQueryReflect(deps ClientDeps, params kgtools.CallToolParams) (bool, kgtools.ToolResult) {
	var a queryReflectArgs
	if err := json.Unmarshal(params.Arguments, &a); err != nil {
		return false, kgtools.ToolResult{}
	}
	// Non-knowledge graphs are out of scope for the thought-domain intercept.
	// Mirror the server-side guard at cmd/knowledge-server/tools/tools_query.go:172-173.
	if a.Graph != "" && a.Graph != "knowledge" {
		return false, kgtools.ToolResult{}
	}
	ctx := context.Background()
	switch a.Mode {
	case "personality":
		return true, handleReflectPersonality(ctx, deps, a)
	case "influence":
		return true, handleReflectInfluence(ctx, deps, a)
	case "tensions":
		return true, handleReflectTensions(ctx, deps, a)
	case "blind_spots":
		return true, handleReflectBlindSpots(ctx, deps, a)
	case "summary":
		return true, handleReflectSummary(ctx, deps, a)
	case "evolution":
		return true, handleReflectEvolution(ctx, deps, a)
	case "clusters":
		return true, handleReflectClusters(ctx, deps, a)
	case "examine":
		// Gate on NodeThought — non-thought IDs fall through to the
		// server's generic inspector. Mirrors the server-side gate at
		// tools_query_inspect.go:63-70 (handleInspectNode thought-branch).
		if a.ID == "" {
			return false, kgtools.ToolResult{}
		}
		gc := deps.GraphCaller()
		if gc == nil {
			return false, kgtools.ToolResult{}
		}
		node, ok := clientthought.FetchNode(ctx, gc, a.ID)
		if !ok || kgtypes.NodeType(node.Type) != kgtypes.NodeThought {
			return false, kgtools.ToolResult{}
		}
		// Re-marshal as the {thought, format} shape handleExamineClient
		// expects. params.Arguments may carry extra fields the
		// handler doesn't read — pass through to keep the existing
		// helper API unchanged.
		examParams := kgtools.CallToolParams{
			Name:      params.Name,
			Arguments: marshalOrEmpty(map[string]any{"thought": a.ID, "format": a.Format}),
		}
		return true, handleExamineClient(ctx, deps, examParams)
	case "simulate":
		return true, handleSimulateClient(ctx, deps, params)
	}

	// Recall routing: query(mode:timeline / mode:charges) and any
	// query(...) carrying a thought-property filter route to recall, so the
	// client claims them here.
	recallModes := map[string]bool{"timeline": true, "charges": true}
	hasThoughtFilter := a.ValenceMin != nil || a.ValenceMax != nil || a.MagnitudeMin != nil ||
		a.ConsistMax != nil || a.Session != "" || a.ConnectedTo != "" || a.Status != ""
	if hasThoughtFilter || recallModes[a.Mode] {
		return true, handleRecallClient(ctx, deps, recallParamsFromQuery(params, a))
	}

	return false, kgtools.ToolResult{}
}

// recallParamsFromQuery translates queryReflectArgs into the recall
// payload shape handleRecallClient expects. Mirrors the server-side
// translation at cmd/knowledge-server/tools/tools_query_routes.go:143-167
// (the routeRecall builder).
func recallParamsFromQuery(params kgtools.CallToolParams, a queryReflectArgs) kgtools.CallToolParams {
	m := map[string]any{
		"query":     a.Text,
		"limit":     a.Limit,
		"mode":      a.Mode,
		"status":    a.Status,
		"all_types": a.Type == "all",
	}
	if a.Session != "" {
		m["session"] = a.Session
	}
	if a.ConnectedTo != "" {
		m["connected_to"] = a.ConnectedTo
	}
	if a.ValenceMin != nil {
		m["valence_min"] = float64(*a.ValenceMin)
	}
	if a.ValenceMax != nil {
		m["valence_max"] = float64(*a.ValenceMax)
	}
	if a.MagnitudeMin != nil {
		m["magnitude_min"] = float64(*a.MagnitudeMin)
	}
	if a.ConsistMax != nil {
		m["consistency_max"] = float64(*a.ConsistMax)
	}
	if a.Format != "" {
		m["format"] = a.Format
	}
	return kgtools.CallToolParams{
		Name:      params.Name,
		Arguments: marshalOrEmpty(m),
	}
}

// marshalOrEmpty marshals data as JSON; returns []byte("{}") on error so
// the downstream JSON unmarshal at the handler entry surfaces a structured
// validation error instead of crashing.
func marshalOrEmpty(data any) json.RawMessage {
	b, err := json.Marshal(data)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return b
}

// propagateArgs is the parsed wire shape for thoughts(propagate, ...). Only
// force_full is decoded — the default path takes no parameters.
//
// ForceFull is a flexBool, NOT a plain bool: stale caller schemas coerce unknown
// params to strings and LLM callers routinely send "true"/"false", so a plain bool
// would silently DROP {"force_full":"true"} (the string fails to decode into a bool
// field, leaving it false) and fall through to the incremental path — exactly the
// silent-degradation failure this guards against. flexBool accepts JSON booleans AND
// the string forms ("true"/"false", case-insensitive), and on any other value (e.g.
// "maybe") its UnmarshalJSON returns an error the handler surfaces LOUDLY rather than
// defaulting.
type propagateArgs struct {
	ForceFull flexBool `json:"force_full"`
	// Similarity triggers the on-demand topic-similarity lever (drain → centroids →
	// reconcile → merge cascade → summaries → drift → links). flexBool for the same
	// mid-session-string tolerance ForceFull has. LinkThreshold / MergeThreshold are
	// optional per-call overrides (flexFloat accepts "0.9" and 0.9); absent → the
	// HIGH package-const defaults. A malformed value is surfaced LOUDLY.
	Similarity     flexBool   `json:"similarity"`
	LinkThreshold  *flexFloat `json:"link_threshold"`
	MergeThreshold *flexFloat `json:"merge_threshold"`
	// Densify overrides for the post-link within-topic kNN densification phase.
	// All optional; absent → the densify*Default consts. DensifyThreshold is
	// flexFloat (accepts "0.93" and 0.93); DensifyK / DensifyEdgeBudget are flexInt
	// (accept "3" and 3). A malformed value is surfaced LOUDLY by the handler.
	DensifyThreshold  *flexFloat `json:"densify_threshold"`
	DensifyK          *flexInt   `json:"densify_k"`
	DensifyEdgeBudget *flexInt   `json:"densify_edge_budget"`
}

// handlePropagateClient runs DeGroot propagation client-side. Returns a
// rendered summary line matching the former server-side output shape.
//
// force_full:true drives the on-demand full-corpus reflection backstop pass via the
// ReflectionForcer (the live PropagationLoop) — bypassing the backstop cadence, the
// quiet-tick skip, and the incremental closure scoping, and resetting the backstop
// clock on completion. That path does NOT claim the reflection guard here:
// ForceFullPass claims it internally, so a double-claim would self-coalesce. A nil
// forcer (reflection loop not running in this process) returns a LOUD error rather
// than silently falling through to the incremental path. A coalesce (another pass
// already in flight) surfaces as the absorbed textResult.
//
// force_full absent/false preserves the existing behavior byte-identically: claim
// the SAME per-account reflection guard the hourly tick holds so a manual propagate
// firing while a tick is mid-pass (or vice-versa) coalesces onto the in-flight pass
// instead of racing a second concurrent recompute + writeback, then run the
// incremental RunPropagation.
func handlePropagateClient(ctx context.Context, deps ClientDeps, params kgtools.CallToolParams) kgtools.ToolResult {
	var a propagateArgs
	// A malformed force_full (a value flexBool cannot read as a bool — e.g. "maybe")
	// is surfaced LOUDLY, never swallowed into the default path: silent degradation on
	// a bad arg is the exact failure class this guards against. An EMPTY payload
	// ({}/null/"") unmarshals cleanly with ForceFull defaulting false → the existing
	// no-arg behavior is preserved.
	if len(params.Arguments) > 0 {
		if err := json.Unmarshal(params.Arguments, &a); err != nil {
			// A malformed force_full / similarity (flexBool), link_threshold /
			// merge_threshold / densify_threshold (flexFloat), or densify_k /
			// densify_edge_budget (flexInt) is surfaced LOUDLY here, never swallowed: the
			// underlying flex error names the offending arg + value.
			return errorResult("propagate: could not parse arguments — force_full/similarity accept a boolean or its string form, link_threshold/merge_threshold/densify_threshold accept a number or numeric string, densify_k/densify_edge_budget accept an integer or its string form: " + err.Error())
		}
	}

	// Readiness gate (bind-first startup): both the force_full and similarity levers need the
	// propagation loop, which is not yet wired during the bind-first window
	// (ReflectionForcer()/SimilarityForcer() return nil then). Gate both here, BEFORE
	// the branch-specific nil-forcer checks, so the transient window is distinguished
	// from the permanent "not running" degrade and a retry succeeds. Plain
	// incremental propagate (neither flag) is UNGATED — it routes via GraphCaller.
	if (bool(a.ForceFull) || bool(a.Similarity)) && !deps.PropReady() {
		return errorResult("thoughts:propagate: daemon still starting — reflection loop not ready yet, retry shortly")
	}

	if bool(a.ForceFull) {
		forcer := deps.ReflectionForcer()
		if forcer == nil {
			return errorResult("propagate force_full: reflection loop not running in this process — cannot force a full backstop pass")
		}
		result, err := forcer.ForceFullPass(ctx)
		if errors.Is(err, clientthought.ErrReflectionInFlight) {
			return textResult("propagate force_full: a reflection pass is already in progress for this account — coalescing onto it")
		}
		if err != nil {
			return errorResult("propagate force_full failed: " + err.Error())
		}
		return textResult("Forced full backstop pass complete: " + formatPropagationResult(result))
	}

	if bool(a.Similarity) {
		return handleSimilarityPass(ctx, deps, a)
	}

	gc := deps.GraphCaller()
	if gc == nil {
		return errorResult("propagate: graph client unavailable")
	}
	release, ok := clientthought.AcquireReflectionPass(clientthought.ReflectionPassKey)
	if !ok {
		return textResult("propagate: a reflection pass is already in progress for this account — coalescing onto it")
	}
	defer release()
	result, err := clientthought.RunPropagation(ctx, gc, nil, nil)
	if err != nil {
		return errorResult("propagate failed: " + err.Error())
	}
	return textResult(formatPropagationResult(result))
}

// headlineCounts maps a finished SimilarityReport's headline counts onto the
// similarity-event metadata keys the completion record carries for at-a-glance
// reads (NOT the full report — that rides content/description).
func headlineCounts(rep clientthought.SimilarityReport) map[string]string {
	return map[string]string{
		clientthought.MetaSimMerges: strconv.Itoa(len(rep.MergeChains)),
		clientthought.MetaSimLinks:  strconv.Itoa(len(rep.LinksCreated)),
		clientthought.MetaSimTopics: strconv.Itoa(rep.TopicCount),
	}
}

// similarityFetchContract renders the trigger/coalesce response contract: the
// verbatim copy-pasteable fetch call, the duration estimate, and the explicit
// may-take-longer caveat. Shared by the trigger, the coalesce path, and the
// in-progress similarity_report fetch so the contract text is identical everywhere.
func similarityFetchContract(estimate string) string {
	return "Fetch the report when it finishes with this exact call:\n" +
		`    thoughts({"operation":"similarity_report"})` + "\n\n" +
		"Estimated duration: " + estimate + "\n" +
		"This is only an estimate — the pass MAY take LONGER than this."
}

// fetchClusterContext reads the loop-persisted cluster state
// (DetectPersistedClusters) + computes personality scalars in one synchronous
// pass. It does NOT recompute clusters live (the loop owns the adjacency+Leiden
// compute) — this keeps the live reflective surface within the tool ceiling.
// Used by every reflective handler that needs clusters or profile state. Returns
// empty values on failure so the format helpers can still render an "empty"
// report. The third return (cold) is TRUE only in the cold case — the corpus is
// non-empty but no node carries cluster_id yet (the loop hasn't completed a pass)
// — so the personality handler can render an explicit not-yet-computed message
// rather than a silent empty profile; a truly empty graph reports cold=false.
func fetchClusterContext(ctx context.Context, deps ClientDeps) ([]clientthought.ThoughtCluster, *clientthought.PersonalityProfile, bool) {
	gc := deps.GraphCaller()
	if gc == nil {
		return nil, nil, false
	}
	clusters, err := clientthought.DetectPersistedClusters(ctx, gc)
	if errors.Is(err, clientthought.ErrClustersNotComputed) {
		return nil, nil, true // cold case: non-empty corpus, no persisted clusters.
	}
	if err != nil || len(clusters) == 0 {
		return clusters, nil, false
	}
	// Feed the charge→evidence adjacency so the cross-cluster attribution leg
	// participates in trust differentiation (nil here left every scalar at 1.000).
	evidenceAdj := clientthought.BuildEvidenceAdj(ctx, gc, clusters)
	profile, err := clientthought.ComputePersonalityScalars(ctx, gc, clusters, evidenceAdj)
	if err != nil {
		return clusters, nil, false
	}
	return clusters, &profile, false
}
