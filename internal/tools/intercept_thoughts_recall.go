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
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"

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
		return errorResult("invalid arguments: " + err.Error())
	}
	if msg := validateRecallClientArgs(a); msg != "" {
		return errorResult(msg)
	}

	// Cluster-mode special case — moved here from interceptThoughtsOp
	// (cmd/knowledge/internal/tools/thought.go:84-87 pre-relocation).
	if a.Mode == "clusters" {
		return handleRecallClusters(ctx, deps, a.AllTypes, a.Format)
	}

	gc := deps.GraphCaller()
	if gc == nil {
		return errorResult("recall: graph client unavailable")
	}

	opts := clientthought.RecallOptions{
		Query:          a.Query,
		ValenceMin:     a.ValenceMin,
		ValenceMax:     a.ValenceMax,
		MagnitudeMin:   a.MagnitudeMin,
		ConsistencyMax: a.ConsistencyMax,
		Status:         a.Status,
		Session:        a.Session,
		ConnectedTo:    a.ConnectedTo,
		Limit:          a.Limit,
	}
	// Route recall candidate-gathering through the CLIENT knowledge
	// segment engines (Manager.Search) UNCONDITIONALLY for a non-empty query — the
	// segment Manager is always wired in the real client, so there is no
	// server-search fallback. Embed the recall query client-side here so the HNSW
	// arm is exercised (the wire Caller is Execute-only and carries no embedder); an
	// empty query vector degrades to the BM25 arm.
	if a.Query != "" {
		opts.Searcher = deps.SegmentManager()
		if emb := deps.Embedder(); emb != nil {
			if vec, err := emb.EmbedBinary(ctx, a.Query); err == nil && len(vec) > 0 {
				opts.QueryVec = vec
			}
		}
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

	results, err := clientthought.RecallThoughts(ctx, gc, opts)
	if err != nil {
		return errorResult("recall failed: " + err.Error())
	}

	if a.Format == "json" {
		return jsonResult(map[string]any{"total": len(results), "thoughts": results})
	}
	mode := a.Mode
	if mode == "" {
		mode = "search"
	}
	return textResult(clientthought.FormatRecallResults(results, mode))
}
