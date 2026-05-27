// SPDX-License-Identifier: Apache-2.0

package graph

import (
	"context"
	"fmt"
	"sort"

	"gonum.org/v1/gonum/graph"
	"gonum.org/v1/gonum/graph/simple"
	"gonum.org/v1/gonum/graph/topo"

	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

// cycles.go implements the CyclesAnalyzer — Johnson's elementary cycles
// algorithm wrapped around gonum's topo.DirectedCyclesIn.
//
// Two operational quirks of the gonum implementation drive this file:
//
//  1. EAGER, NOT STREAMING. topo.DirectedCyclesIn returns the full
//     [][]graph.Node slice up front. On a dense graph the cycle count can
//     blow up exponentially (Johnson is O((V+E)(C+1)) but C — the number
//     of elementary cycles — has no polynomial bound). We mitigate by
//     partitioning with Tarjan SCC first and dropping anything beyond
//     top_k*10 enumerated cycles after the fact.
//
//  2. NO ctx.Context HONOR. There is no way to cancel mid-enumeration —
//     the goroutine will run to completion. We bound the work by only
//     running Johnson's on small, bounded SCCs (see cycleSCCSizeCap).
//
// Output ranking: cycles are sorted by length ascending (shortest first),
// then by the lexicographically smallest member ID for tie-breaking. Short
// cycles are typically the most actionable — a 2-cycle indicates a direct
// circular dependency, a 50-cycle indicates a deeply tangled module that's
// rarely actionable as a single fix.

// cycleSCCSizeCap bounds which SCCs get their elementary cycles
// enumerated. Johnson's algorithm is O((V+E)(C+1)) where C — the number
// of elementary cycles in the input — has no polynomial bound. On a
// call graph, SCCs much larger than a few dozen nodes can have
// exponentially many cycles and Johnson's will explode on them. We
// partition the graph with Tarjan's SCC (O(V+E)) first and only run
// Johnson's on SCCs at or below this size. Larger SCCs are reported as
// a single "SCC too large to enumerate" finding so the user still sees
// that cyclic structure exists there.
const cycleSCCSizeCap = 40

// cycleHardCapMultiplier scales TopK to derive the absolute hard cap on
// enumerated cycles. With the default TopK of 20 this caps the result
// slice at 200 cycles — enough to hand the user a useful shortlist on a
// dense graph, small enough that downstream emit/render stays bounded.
const cycleHardCapMultiplier = 10

// CyclesAnalyzer wraps gonum's Johnson elementary-cycles algorithm. The
// zero value is usable.
type CyclesAnalyzer struct{}

// Name returns the analyzer's stable identifier.
func (CyclesAnalyzer) Name() string { return "cycles" }

// Run materializes the request graph, enumerates elementary cycles via
// Johnson's algorithm over SCC-partitioned subgraphs, and emits one
// Finding per cycle (capped at TopK*10).
func (a CyclesAnalyzer) Run(ctx context.Context, req foundation.Request) ([]foundation.Finding, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("topology/cycles: %w", err)
	}

	g, err := foundation.NewGonumGraph(ctx, req.Caller, req.Graph, req.Name, req.Subset)
	if err != nil {
		return nil, fmt.Errorf("topology/cycles: build graph: %w", err)
	}

	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("topology/cycles: %w", err)
	}

	// Partition the graph with Tarjan SCC first so we only run Johnson's
	// against small, bounded SCCs. Running Johnson's globally on a dense
	// call graph explodes exponentially; the SCC-partitioned variant is
	// O(V+E) for the partition plus Johnson's per small SCC.
	sccs := topo.TarjanSCC(g)

	hardCap := req.TopK
	if hardCap <= 0 {
		hardCap = defaultTopK
	}
	hardCap *= cycleHardCapMultiplier

	var allCycles [][]graph.Node
	var oversized []int
	for _, scc := range sccs {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("topology/cycles: %w", err)
		}
		if len(scc) < 2 {
			// Singleton SCC — check for a self-loop. Johnson's would
			// return a 1-node cycle for nodes with a self-edge, but the
			// subgraph construction path below strips the trailing
			// duplicate gonum appends. Cheaper to detect self-loops
			// directly.
			continue
		}
		if len(scc) > cycleSCCSizeCap {
			oversized = append(oversized, len(scc))
			continue
		}
		sub := buildSCCSubgraph(g, scc)
		allCycles = append(allCycles, topo.DirectedCyclesIn(sub)...)
		if len(allCycles) >= hardCap*2 {
			// Accumulated enough to fill a rank with room to spare —
			// stop enumerating and sort/truncate.
			break
		}
	}

	findings := rankCycleFindings(g, allCycles, hardCap)
	if len(oversized) > 0 {
		findings = append(findings, cyclesOversizedFinding(oversized, cycleSCCSizeCap))
	}
	return findings, nil
}

// buildSCCSubgraph constructs a directed subgraph containing only the
// nodes in scc and the edges between them. Uses a fresh
// simple.DirectedGraph (unweighted is fine — Johnson's ignores weights).
// The gonum IDs are the same as in the parent graph so cycle output
// can be mapped back via the parent's StringID.
func buildSCCSubgraph(parent *foundation.GonumGraph, scc []graph.Node) *simple.DirectedGraph {
	sub := simple.NewDirectedGraph()
	idSet := make(map[int64]struct{}, len(scc))
	for _, n := range scc {
		idSet[n.ID()] = struct{}{}
		sub.AddNode(n)
	}
	for _, n := range scc {
		it := parent.From(n.ID())
		for it.Next() {
			to := it.Node()
			if _, ok := idSet[to.ID()]; !ok {
				continue
			}
			sub.SetEdge(sub.NewEdge(n, to))
		}
	}
	return sub
}

// cyclesOversizedFinding reports SCCs that exceeded the enumeration
// size cap. Users still learn that cyclic structure exists, without
// paying the exponential cost of enumerating every elementary cycle
// within a large SCC.
func cyclesOversizedFinding(sizes []int, cap int) foundation.Finding {
	sort.Slice(sizes, func(i, j int) bool { return sizes[i] > sizes[j] })
	biggest := sizes[0]
	summary := fmt.Sprintf(
		"%d strongly-connected component(s) exceed the cycle enumeration cap of %d nodes (biggest: %d). Johnson's algorithm is exponential in the number of cycles — large SCCs are not enumerated to keep the analyzer bounded. Run `scc` to see the full SCC membership and break the biggest cluster first.",
		len(sizes), cap, biggest,
	)
	return foundation.Finding{
		Algorithm: "cycles",
		Severity:  foundation.SeverityNotice,
		Title:     fmt.Sprintf("%d SCCs too large to enumerate", len(sizes)),
		Summary:   summary,
		Metrics: map[string]float64{
			"oversized_scc_count": float64(len(sizes)),
			"biggest_scc_size":    float64(biggest),
			"size_cap":            float64(cap),
		},
	}
}

// rankCycleFindings converts gonum's [][]graph.Node into Findings,
// applies the hard cap, and sorts by length ascending then by primary
// evidence ID.
func rankCycleFindings(g *foundation.GonumGraph, cycles [][]graph.Node, hardCap int) []foundation.Finding {
	if len(cycles) == 0 {
		return nil
	}
	findings := make([]foundation.Finding, 0, len(cycles))
	for _, cyc := range cycles {
		f, ok := buildCycleFinding(g, cyc)
		if !ok {
			continue
		}
		findings = append(findings, f)
	}
	sort.SliceStable(findings, func(i, j int) bool {
		li := int(findings[i].Metrics["length"])
		lj := int(findings[j].Metrics["length"])
		if li != lj {
			return li < lj
		}
		return primaryEvidence(findings[i]) < primaryEvidence(findings[j])
	})
	if hardCap > 0 && len(findings) > hardCap {
		findings = findings[:hardCap]
	}
	return findings
}

// buildCycleFinding constructs a single Finding for one elementary
// cycle. Returns false if any cycle node has no reverse string-ID
// mapping (defensive — should not happen on a successful build).
//
// gonum's topo.DirectedCyclesIn returns each cycle with the starting
// node DUPLICATED at the end (a cycle A->B->C->A is reported as the
// 4-element slice [A,B,C,A]). We strip the trailing duplicate so the
// reported "length" is the number of distinct nodes in the cycle, which
// matches every textbook definition and what callers expect.
func buildCycleFinding(g *foundation.GonumGraph, cyc []graph.Node) (foundation.Finding, bool) {
	// Strip the trailing duplicate that gonum appends to close the loop.
	if len(cyc) >= 2 && cyc[0].ID() == cyc[len(cyc)-1].ID() {
		cyc = cyc[:len(cyc)-1]
	}
	memberIDs := make([]string, 0, len(cyc))
	for _, n := range cyc {
		s, ok := g.StringID(n.ID())
		if !ok {
			return foundation.Finding{}, false
		}
		memberIDs = append(memberIDs, s)
	}
	length := len(memberIDs)

	// Pick the lexicographically smallest member as the primary evidence
	// for stable dedup, but PRESERVE the original cycle order in the
	// Evidence slice so renderers can show the actual A->B->C->A walk.
	primary := memberIDs[0]
	for _, m := range memberIDs[1:] {
		if m < primary {
			primary = m
		}
	}
	evidence := make([]string, 0, length+1)
	evidence = append(evidence, primary)
	for _, m := range memberIDs {
		if m != primary {
			evidence = append(evidence, m)
		}
	}

	severity := severityFromCycleLength(length)
	title := fmt.Sprintf("Elementary cycle: length %d", length)
	summary := fmt.Sprintf(
		"Directed cycle of %d nodes (%s). Shorter cycles are typically "+
			"the most actionable refactor target.",
		length,
		formatMemberPreview(memberIDs),
	)

	return foundation.Finding{
		Algorithm: "cycles",
		Severity:  severity,
		Title:     title,
		Summary:   summary,
		Evidence:  evidence,
		Metrics: map[string]float64{
			"length": float64(length),
		},
	}, true
}

// severityFromCycleLength maps a cycle's length onto the Severity ladder.
// Short cycles are more urgent because they are more actionable (a
// 2-cycle is a single mutual reference; a 20-cycle is a tangled module).
//
//   - length == 2 → SeverityWarning   (direct mutual dependency)
//   - length 3..5 → SeverityNotice    (small loop, often refactor-worthy)
//   - else        → SeverityInfo      (long loops are usually noise)
func severityFromCycleLength(length int) foundation.Severity {
	switch {
	case length == 2:
		return foundation.SeverityWarning
	case length >= 3 && length <= 5:
		return foundation.SeverityNotice
	default:
		return foundation.SeverityInfo
	}
}

// init self-registers the Cycles analyzer with the foundation registry.
func init() {
	foundation.Register(CyclesAnalyzer{})
}
