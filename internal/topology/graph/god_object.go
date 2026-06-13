// SPDX-License-Identifier: Apache-2.0

package graph

import (
	"context"
	"fmt"
	"log/slog"
	"sort"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

// god_object.go implements GodObjectAnalyzer — a code-graph analyzer that
// surfaces "god types" (classes / structs / interfaces / protocols / traits
// centralizing too much behavior) by computing four Chidamber-Kemerer (CK)
// style metrics per type-ish node and combining them via mean-of-percentiles.
//
// Metrics (helpers in god_object_metrics.go):
//   - CBO   — Coupling Between Objects: distinct OTHER types reached via
//     outgoing USES_TYPE or CALLS edges from the type itself.
//   - RFC   — Response For Class: contained methods + their direct callees,
//     one hop only (strict CK definition per OQ-1).
//   - WMC   — Weighted Methods per Class. PROXY: count of contained methods.
//     True CK WMC weights by cyclomatic complexity, which codegraph doesn't
//     record; the proxy is documented in helpers and in every Summary.
//   - FanIn — incoming CALLS + USES_TYPE + CONTAINS edges. More selective
//     than the generic FanInAnalyzer (which counts every edge type).
//
// Combination: each candidate is percentile-ranked per metric via Percentile;
// combined_percentile is the mean of the four ranks (per OQ-6: mean is robust
// to single-metric noise; max would let one outlier dominate). Severity comes
// from SeverityFromPercentile(combined_percentile).
//
// The analyzer is code-graph-only. Other graph types (knowledge / cloud /
// linkage / practice) get one info-level slog message and a nil result.
// A code graph with zero type-ish nodes (e.g. an unsupported language) gets
// the same treatment.
//
// N+1 avoidance: the four metrics read the candidate set's edges (and the
// contained methods' callee edges) in TWO bulk wire fetches — one over the
// candidate IDs, one over the union of their contained method IDs — then
// group the edges per node in memory. The prior implementation issued a
// per-candidate IterEdges fan-out; the bulk reads preserve the metric
// outputs while collapsing the round-trips.
//
// Type-ish enumeration is hardcoded against the raw tree-sitter NodeType
// strings emitted by the chunker — adding a chunker language requires
// updating typeishNodeTypes below.

// typeishNodeTypes is the set of tree-sitter raw NodeType strings that
// represent class-like declarations across every language the codegraph
// chunker currently supports. The values come directly from the TopLevel
// query patterns in the chunker — keep this list in sync when adding new
// chunker languages.
var typeishNodeTypes = map[kgtypes.NodeType]bool{
	"type_declaration":       true, // Go
	"class_declaration":      true, // TS / Java / C# / Kotlin / PHP
	"class_definition":       true, // Python / C++
	"interface_declaration":  true, // TS / Java / C# / PHP
	"struct_declaration":     true, // C# / Rust
	"struct_type":            true, // Go (tree-sitter inner node)
	"interface_type":         true, // Go (tree-sitter inner node)
	"protocol_declaration":   true, // Swift
	"trait_declaration":      true, // PHP / Rust
	"object_declaration":     true, // Kotlin
	"type_alias_declaration": true, // TS
}

// godObjectMetrics holds the raw CK metrics for one type-ish candidate.
// Percentile ranks are computed in a second pass after every candidate's
// raw values are known, so the percentile fields are populated separately
// in Run() rather than by the helper functions.
type godObjectMetrics struct {
	StringID string
	CBO      int
	RFC      int
	WMC      int
	FanIn    int
}

// GodObjectAnalyzer surfaces type-ish nodes that centralize too much
// behavior (high CBO + RFC + WMC + fan-in) in a code graph. Zero-value
// usable; the analyzer self-registers via init().
type GodObjectAnalyzer struct{}

// Name returns the analyzer's stable identifier.
func (GodObjectAnalyzer) Name() string { return "god_object" }

// Run computes the four CK metrics for every type-ish node in the request
// graph, ranks them by mean-of-percentiles, and emits one Finding per
// top-K ranked candidate.
func (a GodObjectAnalyzer) Run(ctx context.Context, req foundation.Request) ([]foundation.Finding, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("topology/god_object: %w", err)
	}
	if req.Graph != kgtypes.GraphCode {
		slog.Info("topology/god_object: skipping non-code graph",
			"graph_type", req.Graph, "name", req.Name)
		return nil, nil
	}

	g, err := foundation.NewGonumGraph(ctx, req.Caller, req.Graph, req.Name, req.Subset)
	if err != nil {
		return nil, fmt.Errorf("topology/god_object: build graph: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("topology/god_object: %w", err)
	}

	candidates, err := collectGodObjectCandidates(ctx, req, g, req.Language)
	if err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		slog.Info("topology/god_object: no type-ish nodes detected",
			"graph_type", req.Graph, "name", req.Name, "language", req.Language)
		return nil, nil
	}

	rows, err := computeGodObjectRows(ctx, req, candidates)
	if err != nil {
		return nil, err
	}

	return rankGodObjectFindings(ctx, req, rows), nil
}

// languageMatchesScope reports whether a node's persisted Language nodeLang
// falls within the requested scope. Exact equality, plus one family alias:
// scope "typescript" includes both "typescript" and "tsx" (.tsx files
// persist Language "tsx"), so a typescript-scoped topology run does not
// silently drop React/JSX code. Scope "tsx" matches only "tsx" — the alias
// is one-directional. This is the SOLE behavior-driving consumer of the
// "tsx" Language literal; no general alias registry is warranted.
func languageMatchesScope(nodeLang, scope string) bool {
	if nodeLang == scope {
		return true
	}
	return scope == "typescript" && nodeLang == "tsx"
}

// collectGodObjectCandidates enumerates every type-ish node in the
// materialized graph, optionally filtered by the analyzer's language
// scope. The language filter is applied by re-fetching each candidate's
// node and matching its top-level Language field against the scope via
// languageMatchesScope (which folds tsx into a typescript-family scope) —
// the GonumGraph adapter does not carry per-node Language for analyzers in
// general so this is the cheapest place to apply the filter without
// bloating the adapter.
func collectGodObjectCandidates(ctx context.Context, req foundation.Request, g *foundation.GonumGraph, language string) ([]string, error) {
	var ids []string
	for nt := range typeishNodeTypes {
		g.IterateNodesByType(nt, func(_ int64, stringID string) bool {
			ids = append(ids, stringID)
			return true
		})
	}
	if language == "" || len(ids) == 0 {
		return ids, nil
	}
	filtered := make([]string, 0, len(ids))
	for _, id := range ids {
		n, ok, err := foundation.FetchNodeByID(ctx, req.Caller, req.Graph, req.Name, id)
		if err != nil {
			return nil, fmt.Errorf("topology/god_object: lookup %s: %w", id, err)
		}
		if !ok || n == nil {
			continue
		}
		if languageMatchesScope(n.Language, language) {
			filtered = append(filtered, id)
		}
	}
	return filtered, nil
}

// rankGodObjectFindings converts raw metric rows to percentile ranks,
// computes the mean-of-percentiles combined score per row, picks the top
// K via TopK, and builds one Finding per surfaced row. Per-finding name
// resolution and neighbor sampling are performed lazily for the top-K
// rows only.
func rankGodObjectFindings(ctx context.Context, req foundation.Request, rows []godObjectMetrics) []foundation.Finding {
	cboSorted := sortedScores(rows, func(r godObjectMetrics) float64 { return float64(r.CBO) })
	rfcSorted := sortedScores(rows, func(r godObjectMetrics) float64 { return float64(r.RFC) })
	wmcSorted := sortedScores(rows, func(r godObjectMetrics) float64 { return float64(r.WMC) })
	fanInSorted := sortedScores(rows, func(r godObjectMetrics) float64 { return float64(r.FanIn) })

	items := make([]foundation.ScoredItem, len(rows))
	pctByID := make(map[string][4]float64, len(rows))
	combinedByID := make(map[string]float64, len(rows))
	for i, r := range rows {
		pCBO := foundation.Percentile(cboSorted, float64(r.CBO))
		pRFC := foundation.Percentile(rfcSorted, float64(r.RFC))
		pWMC := foundation.Percentile(wmcSorted, float64(r.WMC))
		pFanIn := foundation.Percentile(fanInSorted, float64(r.FanIn))
		combined := (pCBO + pRFC + pWMC + pFanIn) / 4
		pctByID[r.StringID] = [4]float64{pCBO, pRFC, pWMC, pFanIn}
		combinedByID[r.StringID] = combined
		items[i] = foundation.ScoredItem{ID: r.StringID, Score: combined}
	}

	k := req.TopK
	if k <= 0 {
		k = defaultTopK
	}
	top := foundation.TopK(items, k)

	rowByID := make(map[string]godObjectMetrics, len(rows))
	for _, r := range rows {
		rowByID[r.StringID] = r
	}

	findings := make([]foundation.Finding, 0, len(top))
	for _, it := range top {
		row, ok := rowByID[it.ID]
		if !ok {
			continue
		}
		pcts := pctByID[it.ID]
		combined := combinedByID[it.ID]
		display := ResolveNodeName(ctx, req.Caller, req.Graph, req.Name, row.StringID)
		neighbors := sampleNeighbors(ctx, req.Caller, req.Graph, req.Name, row.StringID, "both")
		findings = append(findings, buildGodObjectFinding(row, display, neighbors, pcts, combined))
	}
	return findings
}

// sortedScores extracts a single metric from every row and returns the
// values sorted ascending — the input shape Percentile expects.
func sortedScores(rows []godObjectMetrics, pick func(godObjectMetrics) float64) []float64 {
	out := make([]float64, len(rows))
	for i, r := range rows {
		out[i] = pick(r)
	}
	sort.Float64s(out)
	return out
}

// buildGodObjectFinding constructs a single Finding from one godObjectMetrics
// row plus its pre-computed percentile vector. Severity is derived from the
// combined percentile via SeverityFromPercentile so the urgency thresholds
// stay consistent with every other ranked analyzer.
func buildGodObjectFinding(row godObjectMetrics, displayName string, neighbors []string, pcts [4]float64, combined float64) foundation.Finding {
	pCBO, pRFC, pWMC, pFanIn := pcts[0], pcts[1], pcts[2], pcts[3]
	sev := foundation.SeverityFromPercentile(combined)

	evidence := make([]string, 0, 1+len(neighbors))
	evidence = append(evidence, row.StringID)
	evidence = append(evidence, neighbors...)

	return foundation.Finding{
		Algorithm: "god_object",
		Severity:  sev,
		Title:     fmt.Sprintf("God object: %s", displayName),
		Summary: fmt.Sprintf(
			"CBO=%d (p%.0f), RFC=%d (p%.0f), WMC=%d (p%.0f), fan-in=%d (p%.0f). Combined: top %.2f%%. Centralizing too much — changes ripple.",
			row.CBO, pCBO, row.RFC, pRFC, row.WMC, pWMC, row.FanIn, pFanIn, combined,
		),
		Evidence: evidence,
		Metrics: map[string]float64{
			"cbo":                 float64(row.CBO),
			"cbo_percentile":      pCBO,
			"rfc":                 float64(row.RFC),
			"rfc_percentile":      pRFC,
			"wmc":                 float64(row.WMC),
			"wmc_percentile":      pWMC,
			"fan_in":              float64(row.FanIn),
			"fan_in_percentile":   pFanIn,
			"combined_percentile": combined,
		},
	}
}

func init() {
	foundation.Register(GodObjectAnalyzer{})
}
