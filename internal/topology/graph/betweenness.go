// SPDX-License-Identifier: Apache-2.0

package graph

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"sort"

	"gonum.org/v1/gonum/graph/network"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

// betweenness.go implements SampledBetweennessAnalyzer — a code-graph
// analyzer that surfaces "bridge" nodes: the vertices most shortest paths
// funnel through. Betweenness is a strictly different signal from PageRank:
// PageRank ranks "authorities that many authorities depend on", while
// betweenness ranks "brokers that sit on many shortest routes" — the
// adapters, facades, and glue between subsystems.
//
// Dispatch (see Run):
//
//   - V < exactThreshold (5000): runExact delegates to gonum's
//     network.Betweenness — a reference Brandes 2001 O(V·E) implementation.
//   - Global sampled (default for V ≥ exactThreshold): runSampled uses
//     sampled Brandes (Bader-Madduri 2007) — k uniform sources.
//   - Per-package mode (req.Extra["per_package"]="true" OR automatic
//     fallback when V·sqrt(E) > runtimeCapOps): runPerPackage computes
//     exact betweenness on each NodePackage subgraph and merges top-K.
//
// Code graphs only. Knowledge graphs are also supported; cloud/linkage
// graphs get an info slog line and a nil result. Graph builds use
// NewGonumGraphUnweighted — betweenness has no weight semantics.
// runSampled, runPerPackage, the sampled-Brandes kernel, and the sample-
// size picker helpers all live in betweenness_sampled.go to keep this
// file under the soft line rule.

// exactThreshold is the node count below which runExact runs gonum's
// O(V·E) Brandes. Above this, Run dispatches to runSampled.
const exactThreshold = 5000

// defaultSampleSize is the default k for sampled Brandes — small enough
// to run in milliseconds, large enough for the Bader-Madduri variance
// bound to be useful on graphs of ~10k nodes.
const defaultSampleSize = 100

// maxSampleSize caps k under the adaptive rule min(maxSampleSize, V/50).
const maxSampleSize = 200

// runtimeCapOps is the V*sqrt(E) estimate above which Run falls back
// from global sampled to per-package mode. Package-level var (not const)
// so tests can lower the threshold without building a 100k-node fixture.
var runtimeCapOps float64 = 1e10

// mode* constants label the computation path in a finding's metrics map
// so downstream tooling can tell exact / sampled / per-package apart.
const (
	modeExact      float64 = 0
	modeSampled    float64 = 1
	modePerPackage float64 = 2
)

// SampledBetweennessAnalyzer surfaces "bridge" nodes in a code graph by
// computing (exact for small graphs, sampled Brandes for large graphs)
// vertex betweenness centrality. Zero-value usable; self-registers via
// init().
type SampledBetweennessAnalyzer struct{}

// Name returns the analyzer's stable identifier.
func (SampledBetweennessAnalyzer) Name() string { return "betweenness" }

// Run dispatches to exact, sampled, or per-package betweenness based on
// the request extras, graph size, and runtime budget. It always builds
// the underlying GonumGraph via NewGonumGraphUnweighted — betweenness has
// no weight semantics.
func (a SampledBetweennessAnalyzer) Run(ctx context.Context, req foundation.Request) ([]foundation.Finding, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("topology/betweenness: %w", err)
	}
	// Code graphs are the original target; GraphKnowledge is also
	// supported. Cloud/linkage graphs are still skipped.
	if req.Graph != kgtypes.GraphCode && req.Graph != kgtypes.GraphKnowledge {
		slog.Info("topology/betweenness: skipping unsupported graph",
			"graph_type", req.Graph, "name", req.Name)
		return nil, nil
	}

	if req.Extra["per_package"] == "true" {
		return a.runPerPackage(ctx, req)
	}

	g, err := foundation.NewGonumGraphUnweighted(ctx, req.Caller, req.Graph, req.Name, req.Subset)
	if err != nil {
		return nil, fmt.Errorf("topology/betweenness: build graph: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("topology/betweenness: %w", err)
	}
	if g.Nodes().Len() == 0 {
		return nil, nil
	}

	v := g.Nodes().Len()
	e := g.Edges().Len()
	if estimateOps(v, e) > runtimeCapOps {
		notice := fallbackNotice(v, e)
		pp, perr := a.runPerPackage(ctx, req)
		if perr != nil {
			return nil, perr
		}
		return append([]foundation.Finding{notice}, pp...), nil
	}

	if v < exactThreshold {
		return a.runExact(ctx, req, g)
	}
	return a.runSampled(ctx, req, g)
}

// estimateOps returns a rough cost proxy for betweenness on an unweighted
// graph with V vertices and E edges. Sampled Brandes with k ~ sqrt(E)
// samples is roughly O(V·sqrt(E)).
func estimateOps(v, e int) float64 {
	if v <= 0 || e <= 0 {
		return 0
	}
	return float64(v) * math.Sqrt(float64(e))
}

// fallbackNotice builds a notice-severity Finding explaining that the
// analyzer silently fell back from global sampled to per-package mode.
func fallbackNotice(v, e int) foundation.Finding {
	return foundation.Finding{
		Algorithm: "betweenness",
		Severity:  foundation.SeverityNotice,
		Title:     "Betweenness fallback: per-package mode",
		Summary: fmt.Sprintf(
			"Graph has V=%d, E=%d — estimated V*sqrt(E)=%.2g exceeds runtime "+
				"budget %.2g. Falling back to per-package exact betweenness.",
			v, e, estimateOps(v, e), runtimeCapOps,
		),
		Evidence: []string{},
		Metrics: map[string]float64{
			"mode":           modePerPackage,
			"vertices":       float64(v),
			"edges":          float64(e),
			"ops_estimate":   estimateOps(v, e),
			"runtime_budget": runtimeCapOps,
		},
	}
}

// runExact computes exact vertex betweenness via gonum's
// network.Betweenness. The gonum helper takes a graph.Graph interface,
// which the embedded *simple.WeightedDirectedGraph satisfies via method
// promotion.
func (a SampledBetweennessAnalyzer) runExact(ctx context.Context, req foundation.Request, g *foundation.GonumGraph) ([]foundation.Finding, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("topology/betweenness: %w", err)
	}
	scores := network.Betweenness(g.WeightedDirectedGraph)
	if len(scores) == 0 {
		return nil, nil
	}
	items, allScores := collectBetweennessItems(g, scores)
	return buildBetweennessFindings(ctx, req, items, allScores, findingsBuildConfig{mode: modeExact}), nil
}

// collectBetweennessItems translates gonum int64 IDs back to node string
// IDs, returning (items ready for TopK, sorted-ascending all scores for
// Percentile). Missing translations are silently skipped.
func collectBetweennessItems(g *foundation.GonumGraph, scores map[int64]float64) ([]foundation.ScoredItem, []float64) {
	items := make([]foundation.ScoredItem, 0, len(scores))
	allScores := make([]float64, 0, len(scores))
	for intID, score := range scores {
		stringID, ok := g.StringID(intID)
		if !ok {
			continue
		}
		items = append(items, foundation.ScoredItem{ID: stringID, Score: score})
		allScores = append(allScores, score)
	}
	sort.Float64s(allScores)
	return items, allScores
}

// findingsBuildConfig packages the knobs that vary across computation
// modes (exact / sampled / per-package). Centralizing them lets a single
// builder produce all three finding flavors without duplicating the
// scoring / evidence / percentile loop.
type findingsBuildConfig struct {
	mode           float64
	sampleSize     int     // non-zero only for sampled mode
	sampleVariance float64 // non-zero only for sampled mode
	pkgDisplay     string  // non-empty only for per-package mode
	pkgSize        int     // non-zero only for per-package mode
}

// buildBetweennessFindings ranks items by betweenness, picks the top K,
// and emits one Finding per surfaced node. The config controls title /
// summary / metrics that vary by mode; the core loop is shared.
func buildBetweennessFindings(ctx context.Context, req foundation.Request, items []foundation.ScoredItem, allScores []float64, cfg findingsBuildConfig) []foundation.Finding {
	k := req.TopK
	if k <= 0 {
		k = defaultTopK
	}
	top := foundation.TopK(items, k)
	findings := make([]foundation.Finding, 0, len(top))
	for i, item := range top {
		pct := foundation.Percentile(allScores, item.Score)
		display := ResolveNodeName(ctx, req.Caller, req.Graph, req.Name, item.ID)
		neighbors := sampleNeighbors(ctx, req.Caller, req.Graph, req.Name, item.ID, "both")
		evidence := append(make([]string, 0, 1+len(neighbors)), item.ID)
		evidence = append(evidence, neighbors...)
		findings = append(findings, foundation.Finding{
			Algorithm: "betweenness",
			Severity:  foundation.SeverityFromPercentile(pct),
			Title:     betweennessTitle(display, cfg),
			Summary:   betweennessSummary(item.Score, i+1, len(items), pct, cfg),
			Evidence:  evidence,
			Metrics:   betweennessMetrics(item.Score, i+1, pct, cfg),
		})
	}
	return findings
}

// betweennessTitle renders the Finding title for the chosen mode.
func betweennessTitle(display string, cfg findingsBuildConfig) string {
	if cfg.mode == modePerPackage {
		return fmt.Sprintf("Bridge in %s: %s", cfg.pkgDisplay, display)
	}
	return fmt.Sprintf("Bridge node: %s", display)
}

// betweennessSummary renders the human-readable summary for the chosen mode.
func betweennessSummary(score float64, rank, total int, pct float64, cfg findingsBuildConfig) string {
	if cfg.mode == modePerPackage {
		return fmt.Sprintf(
			"Betweenness %.6f in package %s (size=%d), ranks #%d of %d. Top %.2f%% (mode=per-package).",
			score, cfg.pkgDisplay, cfg.pkgSize, rank, total, pct,
		)
	}
	return fmt.Sprintf(
		"Betweenness %.6f, ranks #%d of %d. Top %.2f%% (mode=%s).",
		score, rank, total, pct, modeLabel(cfg.mode),
	)
}

// betweennessMetrics builds the metrics map for the chosen mode.
func betweennessMetrics(score float64, rank int, pct float64, cfg findingsBuildConfig) map[string]float64 {
	m := map[string]float64{
		"betweenness": score,
		"rank":        float64(rank),
		"percentile":  pct,
		"mode":        cfg.mode,
	}
	if cfg.mode == modeSampled {
		m["sample_size"] = float64(cfg.sampleSize)
		m["sample_variance"] = cfg.sampleVariance
	}
	if cfg.mode == modePerPackage {
		m["package_size"] = float64(cfg.pkgSize)
	}
	return m
}

// modeLabel renders a computation-mode value as a short human string.
func modeLabel(mode float64) string {
	switch mode {
	case modeExact:
		return "exact"
	case modeSampled:
		return "sampled"
	case modePerPackage:
		return "per-package"
	default:
		return "unknown"
	}
}

func init() {
	foundation.Register(SampledBetweennessAnalyzer{})
}
