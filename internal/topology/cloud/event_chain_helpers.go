// SPDX-License-Identifier: Apache-2.0

package cloud

import (
	"fmt"
	"sort"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

// event_chain_helpers.go implements the BFS walk and finding construction
// for the EventChainAnalyzer. Split from event_chain.go to keep both files
// under the line cap. The walk reads adjacency from the in-memory edgeIndex
// built by buildEventChainIndex rather than per-node store queries.

// collectEventSources returns the nodes whose resource_type identifies them
// as event sources, applying the optional subset filter.
func collectEventSources(nodes []*knowledgev1.Node, subset func(*knowledgev1.Node) bool) []*knowledgev1.Node {
	var out []*knowledgev1.Node
	for _, n := range nodes {
		if n == nil {
			continue
		}
		if !eventSourceTypes[metaValue(n, "resource_type")] {
			continue
		}
		if subset != nil && !subset(n) {
			continue
		}
		out = append(out, n)
	}
	return out
}

// eventChainBFS runs a forward BFS from a single event source, tracing
// all reachable event-processing chains. It detects cycles and emits
// separate findings for chains vs circular loops.
func eventChainBFS(idx *edgeIndex, src *knowledgev1.Node, maxDepth int) []foundation.Finding {
	visited := map[string]int{src.Id: 0}
	frontier := []string{src.Id}
	fanOut := 0
	hasCycle := false

	for depth := 1; depth <= maxDepth && len(frontier) > 0; depth++ {
		next := expandEventLayer(idx, frontier, depth, visited, &hasCycle)
		if depth == 1 {
			fanOut = len(next)
		}
		frontier = next
	}

	return buildEventChainFindings(src, visited, fanOut, hasCycle)
}

// expandEventLayer walks one BFS layer following event chain edges.
func expandEventLayer(
	idx *edgeIndex,
	frontier []string,
	depth int,
	visited map[string]int,
	hasCycle *bool,
) []string {
	var next []string
	for _, nodeID := range frontier {
		for _, to := range idx.outgoing(nodeID, eventChainEdges) {
			if _, seen := visited[to]; seen {
				*hasCycle = true
				continue
			}
			visited[to] = depth
			next = append(next, to)
		}
	}
	return next
}

// buildEventChainFindings constructs findings from a completed BFS walk.
func buildEventChainFindings(
	src *knowledgev1.Node,
	visited map[string]int,
	fanOut int,
	hasCycle bool,
) []foundation.Finding {
	chainLen := 0
	for id, d := range visited {
		if id == src.Id {
			continue
		}
		if d > chainLen {
			chainLen = d
		}
	}

	evidence := buildEventEvidence(src.Id, visited)
	name := displayName(src)
	var findings []foundation.Finding

	if hasCycle {
		findings = append(findings, foundation.Finding{
			Algorithm: "event_chain",
			Severity:  foundation.SeverityWarning,
			Title:     fmt.Sprintf("Circular event loop detected: %s", name),
			Summary: fmt.Sprintf(
				"Event source %s (%s) has a circular event chain.",
				name, metaValue(src, "resource_type"),
			),
			Evidence: evidence,
			Metrics: map[string]float64{
				"chain_length": float64(chainLen),
				"fan_out":      float64(fanOut),
			},
		})
	}

	if chainLen > 0 || fanOut > 0 {
		severity := classifyEventChainSeverity(chainLen)
		findings = append(findings, foundation.Finding{
			Algorithm: "event_chain",
			Severity:  severity,
			Title:     fmt.Sprintf("Event chain: %s (depth %d, fan-out %d)", name, chainLen, fanOut),
			Summary: fmt.Sprintf(
				"Event source %s (%s) drives a chain %d hops deep with fan-out %d.",
				name, metaValue(src, "resource_type"), chainLen, fanOut,
			),
			Evidence: evidence,
			Metrics: map[string]float64{
				"chain_length": float64(chainLen),
				"fan_out":      float64(fanOut),
			},
		})
	}

	return findings
}

// buildEventEvidence constructs the evidence list: source first, then
// remaining visited nodes sorted by depth.
func buildEventEvidence(srcID string, visited map[string]int) []string {
	type distNode struct {
		id   string
		dist int
	}
	ranked := make([]distNode, 0, len(visited)-1)
	for id, dist := range visited {
		if id == srcID {
			continue
		}
		ranked = append(ranked, distNode{id, dist})
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].dist != ranked[j].dist {
			return ranked[i].dist < ranked[j].dist
		}
		return ranked[i].id < ranked[j].id
	})
	evidence := make([]string, 0, 1+len(ranked))
	evidence = append(evidence, srcID)
	for _, r := range ranked {
		evidence = append(evidence, r.id)
	}
	return evidence
}

// classifyEventChainSeverity maps chain length to severity.
func classifyEventChainSeverity(chainLen int) foundation.Severity {
	switch {
	case chainLen >= eventChainWarningLength:
		return foundation.SeverityWarning
	case chainLen >= eventChainNoticeLength:
		return foundation.SeverityNotice
	default:
		return foundation.SeverityInfo
	}
}

// sortEventChainFindings orders findings: warnings first, then by
// chain_length descending, then by primary evidence for stability.
func sortEventChainFindings(findings []foundation.Finding) {
	sevOrder := map[foundation.Severity]int{
		foundation.SeverityWarning: 0,
		foundation.SeverityNotice:  1,
		foundation.SeverityInfo:    2,
	}
	sort.SliceStable(findings, func(i, j int) bool {
		si := sevOrder[findings[i].Severity]
		sj := sevOrder[findings[j].Severity]
		if si != sj {
			return si < sj
		}
		ci := findings[i].Metrics["chain_length"]
		cj := findings[j].Metrics["chain_length"]
		if ci != cj {
			return ci > cj
		}
		return primaryEvidence(findings[i]) < primaryEvidence(findings[j])
	})
}
