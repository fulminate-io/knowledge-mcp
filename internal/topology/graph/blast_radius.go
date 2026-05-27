// SPDX-License-Identifier: Apache-2.0

package graph

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

// blast_radius.go implements BlastRadiusAnalyzer — a reverse-BFS topology
// analyzer that answers the question "if this node breaks, what else
// breaks with it?"
//
// REVERSE DIRECTION — the BFS walks INCOMING edges from the seed. A node
// with an incoming edge "depends on" the seed in the source direction, so
// walking backward enumerates everything downstream of a failure. The
// incoming dependents of a frontier layer are read in one bulk wire edge
// fetch over the layer and filtered to edges whose ToId is in the layer.
//
// REDUNDANCY WEIGHTING — every visited intermediate node is run through
// the redundancyFactor lookup (blast_radius_rules.go). A workload with 4
// replicas contributes 0.25 of a hop instead of 1.0; an LB fronting 3
// target groups contributes 1/3. The per-hop contribution is then divided
// by the BFS depth so distant dependents matter less than direct ones.
//
// SEEDLESS MODE — when req.Extra["start"] is empty the analyzer composes
// with the registered "pagerank" analyzer (see seedsFromPageRank below).
// PageRank's top-K nodes become blast-radius seeds, producing one Finding
// per seed.
//
// CONFIGURATION — every knob lives in req.Extra so the analyzer can be
// reused across cloud, code, and knowledge graphs without forcing new
// fields onto the Request struct:
//
//   - "start"      — comma-separated seed node IDs. Empty → seedless mode.
//   - "edge_types" — comma-separated EdgeType override. Empty → defaults
//     per req.Graph (see reverseEdgeTypesFor).
//   - "max_depth"  — BFS depth cap. Empty or invalid → defaultMaxDepth.
//
// SEVERITY — percentile-based per OQ-1. With multiple seeds the analyzer
// ranks every seed's blast_score and maps it through SeverityFromPercentile.
// With a single seed there is no population to compare against, so the lone
// finding is emitted as SeverityInfo and the operator decides.

const (
	// defaultMaxDepth caps the reverse BFS at a reasonable transitive
	// horizon. Anything beyond ~10 hops in production graphs is usually
	// noise (the entire reachable component) and the marginal cost of
	// each additional layer is super-linear.
	defaultMaxDepth = 10
	// blastEvidenceMax caps how many top-impact dependents are recorded
	// in Finding.Evidence beyond the seed itself.
	blastEvidenceMax = 10
)

// BlastRadiusAnalyzer ranks nodes by the redundancy-weighted size of
// their reverse-dependency tree. Zero-value usable; self-registers via
// init().
type BlastRadiusAnalyzer struct{}

// Name returns the analyzer's stable identifier. Findings emitted by Run
// carry this in their Algorithm field. The literal "blast_radius" is the
// dedup discriminator used downstream.
func (BlastRadiusAnalyzer) Name() string { return "blast_radius" }

// Run executes the reverse-BFS analyzer against the request. Honors
// req.Extra["start"] for explicit seeds, falls back to PageRank in
// seedless mode, and respects req.TopK to cap the returned slice.
func (a BlastRadiusAnalyzer) Run(ctx context.Context, req foundation.Request) ([]foundation.Finding, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("topology/blast_radius: %w", err)
	}
	if req.Caller == nil {
		return nil, fmt.Errorf("topology/blast_radius: req.Caller must not be nil")
	}

	seeds, err := resolveSeeds(ctx, req)
	if err != nil {
		return nil, err
	}
	if len(seeds) == 0 {
		return nil, nil
	}

	edgeTypes := resolveEdgeTypes(req)
	if len(edgeTypes) == 0 {
		return nil, nil
	}
	maxDepth := resolveMaxDepth(req)

	findings := make([]foundation.Finding, 0, len(seeds))
	for _, seed := range seeds {
		if cerr := ctx.Err(); cerr != nil {
			return nil, fmt.Errorf("topology/blast_radius: %w", cerr)
		}
		f, runErr := runBlastFromSeed(ctx, req, seed, edgeTypes, maxDepth)
		if runErr != nil {
			return nil, runErr
		}
		findings = append(findings, f)
	}

	applyMultiSeedSeverity(findings)

	sort.SliceStable(findings, func(i, j int) bool {
		return findings[i].Metrics["blast_score"] > findings[j].Metrics["blast_score"]
	})
	if req.TopK > 0 && len(findings) > req.TopK {
		findings = findings[:req.TopK]
	}
	return findings, nil
}

// resolveSeeds parses req.Extra["start"] when set, or falls back to the
// PageRank-driven seedless path. The empty / pagerank-missing case returns
// (nil, nil) so Run can short-circuit cleanly.
func resolveSeeds(ctx context.Context, req foundation.Request) ([]string, error) {
	raw := ""
	if req.Extra != nil {
		raw = req.Extra["start"]
	}
	if raw == "" {
		return seedsFromPageRank(ctx, req, defaultTopK)
	}
	parts := strings.Split(raw, ",")
	seeds := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, p := range parts {
		s := strings.TrimSpace(p)
		if s == "" {
			continue
		}
		if _, dup := seen[s]; dup {
			continue
		}
		seen[s] = struct{}{}
		seeds = append(seeds, s)
	}
	return seeds, nil
}

// resolveEdgeTypes returns the override list from req.Extra["edge_types"]
// when present, or the hardcoded default list keyed by req.Graph. Empty
// override strings parse to nil so the caller can detect "no edges,
// nothing to walk" and short-circuit.
func resolveEdgeTypes(req foundation.Request) []kgtypes.EdgeType {
	if override := parseEdgeTypeOverride(req); len(override) > 0 {
		return override
	}
	return reverseEdgeTypesFor(req.Graph)
}

// parseEdgeTypeOverride parses req.Extra["edge_types"] into a slice of
// EdgeType values, trimming whitespace and dropping empty entries.
// Returns nil when no override is supplied so the caller can detect the
// fall-through case cleanly.
func parseEdgeTypeOverride(req foundation.Request) []kgtypes.EdgeType {
	if req.Extra == nil {
		return nil
	}
	raw := req.Extra["edge_types"]
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]kgtypes.EdgeType, 0, len(parts))
	for _, p := range parts {
		s := strings.TrimSpace(p)
		if s == "" {
			continue
		}
		out = append(out, kgtypes.EdgeType(s))
	}
	return out
}

// reverseEdgeTypesFor returns the default reverse-BFS edge type list for
// each graph type. Decisions baked in here:
//
//   - Cloud: every collector edge type that represents a real dependency.
//     Listed explicitly (rather than "all edges") to keep the BFS away
//     from cosmetic relationships like OWNED_BY-only k8s linkage that
//     produces noisy reachability.
//   - Code: CALLS only — the canonical "X depends on Y" edge.
//   - Knowledge: relates-to + contains + informed-by + produced.
//   - Linkage / Practice: knowledge fallback list (these graphs are
//     small enough that the same set is fine).
func reverseEdgeTypesFor(g kgtypes.GraphType) []kgtypes.EdgeType {
	switch g {
	case kgtypes.GraphCloud:
		return []kgtypes.EdgeType{
			kgtypes.EdgeUsesNetwork,
			kgtypes.EdgeUsesSubnet,
			kgtypes.EdgeUsesSA,
			kgtypes.EdgeUsesPVC,
			kgtypes.EdgeUsesSecurityGroup,
			kgtypes.EdgeTargets,
			kgtypes.EdgeRoutesTo,
			kgtypes.EdgeAssumesRole,
			kgtypes.EdgeMountsSecret,
			kgtypes.EdgeMountsConfigMap,
			kgtypes.EdgeBoundTo,
			kgtypes.EdgeOwnedBy,
			kgtypes.EdgeSelects,
			kgtypes.EdgeContains,
			kgtypes.EdgeMonitors,
			kgtypes.EdgeUsesImage,
			kgtypes.EdgeTrusts,
			kgtypes.EdgeSharedWith,
		}
	case kgtypes.GraphCode:
		return []kgtypes.EdgeType{kgtypes.EdgeCalls}
	default:
		return []kgtypes.EdgeType{
			kgtypes.EdgeRelatesTo,
			kgtypes.EdgeKGContains,
			kgtypes.EdgeInformedBy,
			kgtypes.EdgeProduced,
		}
	}
}

// resolveMaxDepth reads the BFS depth cap from req.Extra. Out-of-range
// values fall back to defaultMaxDepth so a misconfigured caller can
// never silently produce an unbounded walk.
func resolveMaxDepth(req foundation.Request) int {
	if req.Extra == nil {
		return defaultMaxDepth
	}
	v := foundation.ExtraFloat(req, "max_depth", float64(defaultMaxDepth), func(f float64) bool {
		return f >= 1 && f <= 100
	})
	return int(v)
}

// applyMultiSeedSeverity recomputes Finding.Severity using a percentile
// pass over every seed's blast_score. With a single finding the per-seed
// severity is left as-is (SeverityInfo from buildBlastFinding). Mutates
// findings in place to keep Run's body small.
func applyMultiSeedSeverity(findings []foundation.Finding) {
	if len(findings) < 2 {
		return
	}
	scores := make([]float64, len(findings))
	for i, f := range findings {
		scores[i] = f.Metrics["blast_score"]
	}
	sort.Float64s(scores)
	for i := range findings {
		pct := foundation.Percentile(scores, findings[i].Metrics["blast_score"])
		findings[i].Severity = foundation.SeverityFromPercentile(pct)
		findings[i].Metrics["percentile"] = pct
	}
}

// init self-registers BlastRadiusAnalyzer with the foundation registry so
// callers can look it up by name without importing this file directly.
func init() {
	foundation.Register(BlastRadiusAnalyzer{})
}
