// SPDX-License-Identifier: Apache-2.0

// intercept_thoughts_recall.go — client-side claim for
// thoughts(operation:recall). Translates the recall payload into a
// clientthought.RecallThoughts call against the wire helpers from Phase 2.
// The cluster-mode special case is preserved by early-returning to
// handleRecallClusters (mirrors the server-side branch in
// tools_thought_query.go:91-98 pre-relocation).

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/rerank"

	clientthought "github.com/fulminate-io/knowledge-mcp/internal/thought"
)

// recallClientArgs is the parsed thoughts(operation:recall) shape. Mirrors
// the server-side recallArgs at tools_thought_query.go:32-47 — same field
// set, json tags identical. Decoded via plain float64 / int (no flexFloat/
// flexInt) because client callers pass the same wire shape.
type recallClientArgs struct {
	Query          string   `json:"query"`
	ValenceMin     *float64 `json:"valence_min"`
	ValenceMax     *float64 `json:"valence_max"`
	MagnitudeMin   float64  `json:"magnitude_min"`
	ConsistencyMax *float64 `json:"consistency_max"`
	Status         string   `json:"status"`
	Session        string   `json:"session"`
	ConnectedTo    string   `json:"connected_to"`
	TimeStart      string   `json:"time_start"`
	TimeEnd        string   `json:"time_end"`
	Mode           string   `json:"mode"`
	AllTypes       bool     `json:"all_types"`
	Limit          int      `json:"limit"`
	Format         string   `json:"format"`
}

// validateRecallClientArgs surfaces a structured error for out-of-range
// scalar filters. Mirrors validateRecallArgs at tools_thought_query.go:51-65.
func validateRecallClientArgs(a recallClientArgs) string {
	if a.ValenceMin != nil && (*a.ValenceMin < -1 || *a.ValenceMin > 1) {
		return fmt.Sprintf("recall: valence_min %.2f is out of range (must be in [-1, 1])", *a.ValenceMin)
	}
	if a.ValenceMax != nil && (*a.ValenceMax < -1 || *a.ValenceMax > 1) {
		return fmt.Sprintf("recall: valence_max %.2f is out of range (must be in [-1, 1])", *a.ValenceMax)
	}
	if a.ConsistencyMax != nil && (*a.ConsistencyMax < 0 || *a.ConsistencyMax > 1) {
		return fmt.Sprintf("recall: consistency_max %.2f is out of range (must be in [0, 1])", *a.ConsistencyMax)
	}
	if a.ValenceMin != nil && a.ValenceMax != nil && *a.ValenceMin > *a.ValenceMax {
		return fmt.Sprintf("recall: valence_min %.2f > valence_max %.2f — bounds are swapped (range can never match)", *a.ValenceMin, *a.ValenceMax)
	}
	return ""
}

// handleRecallClient claims thoughts(operation:recall). The cluster-mode
// special case dispatches to handleRecallClusters; every other path runs
// the full RecallThoughts pipeline against the wire helpers and renders
// via FormatRecallResults.
func handleRecallClient(ctx context.Context, deps ClientDeps, params kgtools.CallToolParams) kgtools.ToolResult {
	var a recallClientArgs
	if err := json.Unmarshal(params.Arguments, &a); err != nil {
		return errorResult("invalid arguments: " + decodeArgsError(params.Arguments, err))
	}
	if msg := validateRecallClientArgs(a); msg != "" {
		return errorResult(msg)
	}

	// Cluster-mode special case — moved here from interceptThoughtsOp
	// (cmd/knowledge/internal/tools/thought.go:84-87 pre-relocation).
	if a.Mode == "clusters" {
		return handleRecallClusters(ctx, deps, a.AllTypes, a.Format)
	}

	// Context-pack special case — mode:context composes five client-side read
	// primitives (cross-type seed search, bounded edge expansion, charge state,
	// recency overlay, open tickets) into one bounded, deterministically-ordered
	// context pack. Same op-dispatch idiom as the clusters branch above: a mode
	// arm dispatched BEFORE the thought-only RecallThoughts pipeline.
	if a.Mode == "context" {
		return handleRecallContext(ctx, deps, a)
	}

	gc := deps.GraphCaller()
	if gc == nil {
		return errorResult("recall: graph client unavailable")
	}

	// ONE clamp at the shared site, BEFORE the query/bare branch below. The
	// declared maximum is unconditional, so its enforcement must be too: a clamp
	// wired only into the query path's rerank trim would leave bare recall passing
	// the caller's limit straight through to RecallThoughts.
	effLimit, limitClamped := clampCallerLimit(a.Limit)

	opts := clientthought.RecallOptions{
		Query:          a.Query,
		ValenceMin:     a.ValenceMin,
		ValenceMax:     a.ValenceMax,
		MagnitudeMin:   a.MagnitudeMin,
		ConsistencyMax: a.ConsistencyMax,
		Status:         a.Status,
		Session:        a.Session,
		ConnectedTo:    a.ConnectedTo,
		Limit:          effLimit,
	}
	// Construct the rerank seam at the call site, through the shared
	// tools-package helper both rerank sites use: a missing credential on the
	// resolved [reranker] axis gates the reranker to nil, which
	// rerankRecallResults degrades to RRF ordering on — the same contract as
	// before, now read from that axis's own provider rather than from a single
	// Voyage key. A malformed section also yields nil, with the error logged.
	// Done unconditionally so didRerank threading is uniform; the reranker is
	// only used on the a.Query != "" branch below.
	reranker := buildReranker(ctx, widePoolSize, widePoolTopK)
	if a.Query != "" {
		configureRecallQueryPath(ctx, deps, a.Query, &opts)
	}
	if a.TimeStart != "" {
		t, err := time.Parse("2006-01-02", a.TimeStart)
		if err != nil {
			return errorResult(fmt.Sprintf("recall: time_start %q is not a valid date (expected YYYY-MM-DD)", a.TimeStart))
		}
		opts.TimeStart = t
	}
	if a.TimeEnd != "" {
		t, err := time.Parse("2006-01-02", a.TimeEnd)
		if err != nil {
			return errorResult(fmt.Sprintf("recall: time_end %q is not a valid date (expected YYYY-MM-DD)", a.TimeEnd))
		}
		opts.TimeEnd = t
	}

	opts.Source = corpusSourceFromDeps(deps) // bare-recall fallback reads the resident cache when warm.
	results, err := clientthought.RecallThoughts(ctx, gc, opts)
	if err != nil {
		return errorResult("recall failed: " + err.Error())
	}

	// Rerank the untrimmed wide pool then trim to the caller-visible limit —
	// query path only (the bare path has no query to rerank and keeps its
	// magnitude-sorted full-corpus drain). Mirrors search's
	// rerank-then-trim (search_rerank.go:52,62).
	didRerank := false
	if a.Query != "" {
		results, didRerank = rerankAndTrimRecall(ctx, a.Query, results, reranker, effLimit)
	}

	return renderRecallResults(a, results, didRerank, limitClamped)
}

// renderRecallResults renders the recall result set in the caller's requested
// format. Both formats disclose an engaged limit clamp — the json branch through
// a limit_clamped key alongside total, the text branch through an appended
// notice — because a declaration the caller can read must not be enforced
// silently in either render.
func renderRecallResults(
	a recallClientArgs, results []clientthought.ThoughtResult, didRerank, limitClamped bool,
) kgtools.ToolResult {
	if a.Format == "json" {
		payload := map[string]any{"total": len(results), "thoughts": results}
		if limitClamped {
			payload["limit_clamped"] = true
		}
		return jsonResult(payload)
	}
	mode := a.Mode
	if mode == "" {
		mode = "search"
	}
	rendered := clientthought.FormatRecallResults(results, mode, didRerank)
	if limitClamped {
		rendered += "\n\n" + recallLimitClampNotice
	}
	return textResult(rendered)
}

// recallLimitClampNotice is the caller-facing disclosure that the declared
// `limit` maximum engaged. Its twin on the search arm (searchLimitClampNotice,
// search_rerank.go) carries the same copy: the text is duplicated rather than
// shared so each arm's disclosure is greppable at its own site.
const recallLimitClampNotice = "Showing 50 results — the declared `limit` maximum of 50 engaged, so this result may be incomplete."

// configureRecallQueryPath wires the query-path gather options: route through
// the CLIENT knowledge segment engines (Manager.Search) — there is no
// server-search fallback. The segment Manager is wired for the life of the daemon
// EXCEPT during the bind-first wiring window (bind-first startup): recall is UNGATED by
// design, so a nil Searcher set here degrades downstream to full-corpus iteration
// (searchRecallCandidates early-returns no candidates on a nil Searcher) rather
// than gating on PipelineReady. This widens the gather to search's exact pool
// width so RecallThoughts returns the UNTRIMMED filtered+sorted wide pool (the
// rerank can then promote a buried candidate before the caller trims), and embeds
// the query client-side so the HNSW arm is exercised. An empty query vector
// degrades to the BM25 arm; a nil embedder is logged as the BM25-only/HNSW-down
// diagnostic.
func configureRecallQueryPath(ctx context.Context, deps ClientDeps, query string, opts *clientthought.RecallOptions) {
	opts.Searcher = deps.SegmentManager()
	opts.WidePool = widePoolSize
	emb := deps.Embedder()
	if emb == nil {
		// Diagnostic residual: a logged-in daemon that lost its embedder is
		// silently BM25-only — surface it in the log instead of leaving it to be
		// inferred from flat scores. No behavior change: the empty QueryVec already
		// degrades to the BM25 arm.
		slog.Warn("recall: no embedder on query path — HNSW arm down, BM25-only gather", "query_len", len(query))
		return
	}
	// The embed ERROR gets the same diagnostic the nil-embedder case above gets.
	// It used to be discarded, so a CONFIGURED embedder that failed at call time was
	// strictly quieter than having no embedder at all — the louder condition
	// produced a warning and the more surprising one produced silence.
	vec, err := emb.EmbedBinary(ctx, query)
	switch {
	case err != nil:
		slog.Warn("recall: query embed failed — HNSW arm down, BM25-only gather",
			"error", err, "query_len", len(query))
	case len(vec) > 0:
		opts.QueryVec = vec
	}
}

// rerankAndTrimRecall reranks the untrimmed wide pool through the supplied
// reranker (nil degrades to RRF, didRerank=false) and trims to the caller-visible
// limit AFTER rerank — mirroring search's rerank-then-trim (search_rerank.go:62).
func rerankAndTrimRecall(
	ctx context.Context, query string, results []clientthought.ThoughtResult, reranker rerank.Reranker, limit int,
) ([]clientthought.ThoughtResult, bool) {
	results, didRerank := rerankRecallResults(ctx, query, results, reranker)
	if limit <= 0 {
		limit = 20 // matches RecallThoughts' default (query.go:90-92).
	}
	if len(results) > limit {
		results = results[:limit]
	}
	return results, didRerank
}
