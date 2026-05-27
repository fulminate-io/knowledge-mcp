// SPDX-License-Identifier: Apache-2.0

package graph

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"gonum.org/v1/gonum/graph"
	"gonum.org/v1/gonum/graph/topo"

	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

// scc.go implements the SCCAnalyzer — a thin wrapper around gonum's
// topo.TarjanSCC that surfaces non-trivial strongly-connected components
// as foundation.Findings. SCCs of size >= 2 represent dependency cycles
// (mutual recursion in code, circular references in knowledge, dependency
// loops in cloud), each of which becomes one Finding.
//
// Self-loop policy (OQ-F, fixed default): single-node SCCs are ALWAYS
// excluded, even when the lone node has a self-loop edge. This drops noisy
// "function calls itself" recursion findings on CALLS graphs and matches
// the ticket's requested default. The decision to keep this as a fixed
// default (no per-request knob) was recorded against open question
// b7745d9e7935895b736a05c134ceb4da. A future ticket may add a Request.Extra
// field to expose include_self_loops as an opt-in.
//
// Severity is derived from the SCC size:
//
//   - size >= 5 → SeverityCritical
//   - size 3..4 → SeverityWarning
//   - size 2    → SeverityNotice
//
// We do NOT use Percentile here: SCC severity is an absolute property of
// the cycle's complexity, not a relative position in any distribution.

// SCCAnalyzer wraps gonum's Tarjan SCC algorithm. Zero-value usable.
type SCCAnalyzer struct{}

// Name returns the analyzer's stable identifier.
func (SCCAnalyzer) Name() string { return "scc" }

// Run materializes the request graph, runs Tarjan's SCC algorithm via
// gonum, and emits one Finding per non-trivial strongly-connected
// component. Single-node SCCs are dropped unconditionally per the
// self-loop policy documented at the top of this file.
func (a SCCAnalyzer) Run(ctx context.Context, req foundation.Request) ([]foundation.Finding, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("topology/scc: %w", err)
	}

	g, err := foundation.NewGonumGraph(ctx, req.Caller, req.Graph, req.Name, req.Subset)
	if err != nil {
		return nil, fmt.Errorf("topology/scc: build graph: %w", err)
	}

	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("topology/scc: %w", err)
	}

	sccs := topo.TarjanSCC(g)
	findings := make([]foundation.Finding, 0, len(sccs))
	for _, scc := range sccs {
		if len(scc) < 2 {
			// Single-node SCC: dropped unconditionally, even if a
			// self-loop edge is present. See file-level docs for the
			// policy rationale (OQ-F).
			continue
		}
		findings = append(findings, buildSCCFinding(g, scc))
	}

	// Stable, deterministic output: largest SCCs first, then by primary
	// evidence ID for ties. Callers downstream rely on a stable order.
	sort.SliceStable(findings, func(i, j int) bool {
		si := int(findings[i].Metrics["size"])
		sj := int(findings[j].Metrics["size"])
		if si != sj {
			return si > sj
		}
		return primaryEvidence(findings[i]) < primaryEvidence(findings[j])
	})

	return findings, nil
}

// buildSCCFinding constructs a single Finding for one strongly-connected
// component. The SCC's member node IDs become the Evidence slice (sorted
// for determinism); the first element is treated as the primary evidence
// for dedup.
func buildSCCFinding(g *foundation.GonumGraph, scc []graph.Node) foundation.Finding {
	memberIDs := make([]string, 0, len(scc))
	for _, n := range scc {
		stringID, ok := g.StringID(n.ID())
		if !ok {
			// Node was materialized into the graph but somehow has no
			// reverse mapping — this should be impossible after a
			// successful NewGonumGraph build, but we drop it defensively
			// rather than emitting an empty evidence string.
			continue
		}
		memberIDs = append(memberIDs, stringID)
	}
	sort.Strings(memberIDs)

	size := len(memberIDs)
	severity := severityFromSCCSize(size)
	title := fmt.Sprintf("Dependency cycle: %d-node SCC", size)
	summary := fmt.Sprintf(
		"Strongly-connected component of %d nodes — every node in this set "+
			"can reach every other node, indicating a dependency cycle. "+
			"Members: %s",
		size,
		formatMemberPreview(memberIDs),
	)

	return foundation.Finding{
		Algorithm: "scc",
		Severity:  severity,
		Title:     title,
		Summary:   summary,
		Evidence:  memberIDs,
		Metrics: map[string]float64{
			"size": float64(size),
		},
	}
}

// severityFromSCCSize maps SCC size onto the topology Severity ladder.
// The thresholds are absolute (not percentile-based) because SCC size is
// an inherent property of the cycle's complexity rather than a relative
// rank within a distribution.
func severityFromSCCSize(size int) foundation.Severity {
	switch {
	case size >= 5:
		return foundation.SeverityCritical
	case size >= 3:
		return foundation.SeverityWarning
	default:
		return foundation.SeverityNotice
	}
}

// formatMemberPreview returns a compact, deterministic string of the
// first few SCC member IDs for inclusion in a finding summary.
// Truncation keeps long SCCs from blowing out renderer width while
// still giving the reader a useful preview.
func formatMemberPreview(members []string) string {
	const maxPreview = 5
	if len(members) <= maxPreview {
		return strings.Join(members, ", ")
	}
	preview := strings.Join(members[:maxPreview], ", ")
	return fmt.Sprintf("%s, +%d more", preview, len(members)-maxPreview)
}

// init self-registers the SCC analyzer with the foundation registry so
// callers can look it up by name without importing this file directly.
func init() {
	foundation.Register(SCCAnalyzer{})
}
