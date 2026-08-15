// SPDX-License-Identifier: Apache-2.0

package engine

import (
	"fmt"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// render_edge_groups.go holds the shared TEXT renderer for multi-candidate edge
// groups, used by the traverse text arm, by the analyze arm and by the explain
// and correlations arms.
//
// A group is ONE reference whose target could not be bound to a single
// declaration. It renders as ONE block listing every candidate, never as N
// independent edges — restating N alternatives as N facts is exactly the defect
// this representation exists to remove.
//
// GROUP BLOCKS RENDER WHENEVER GROUPS EXIST, INDEPENDENT OF THE CALLER'S
// include_edge_metadata. A caller who never asked for edge metadata still must
// not be shown N alternatives as N facts, which is why code-graph traversals
// request the carrier for themselves (compileTraverse).

// writeCandidateGroups renders one block per group into sb.
//
// nodes supplies hydrated candidate nodes keyed by node ID, for the three facts
// the representation requires per candidate: node ID, file path with line, and
// signature. A candidate missing from nodes renders its ID alone, marked
// (unhydrated) — never fabricated and never silently dropped, because a
// candidate the reader cannot see is a candidate the reader cannot validate.
//
// reached marks which candidates the current walk actually arrived at. On a
// forward walk from the group's source that is all of them; on a reverse walk it
// is exactly one, and marking it is what makes the block readable from the far
// side. Surfaces with no walk pass reached=nil and the marker never appears.
//
// ZERO GROUPS PRODUCE ZERO BYTES, mirroring writeEdgeMetadataSection's empty
// early return — that is what keeps a group-free response byte-identical to
// today's output.
func writeCandidateGroups(sb *strings.Builder, groups []CandidateGroup, nodes map[string]*knowledgev1.Node, reached map[string]bool) {
	if len(groups) == 0 {
		return
	}
	for i := range groups {
		writeCandidateGroup(sb, &groups[i], nodes, reached, true)
	}
}

// writeCandidateGroupsNoFrontier renders the same blocks WITHOUT the frontier
// statement, for surfaces that have no walk to stop: a single-node listing or a
// whole-graph table cannot "continue" from a candidate, so claiming a traversal
// stopped there would describe a walk that never happened.
func writeCandidateGroupsNoFrontier(sb *strings.Builder, groups []CandidateGroup, nodes map[string]*knowledgev1.Node, reached map[string]bool) {
	if len(groups) == 0 {
		return
	}
	for i := range groups {
		writeCandidateGroup(sb, &groups[i], nodes, reached, false)
	}
}

// writeCandidateGroup renders exactly one group block.
func writeCandidateGroup(sb *strings.Builder, g *CandidateGroup, nodes map[string]*knowledgev1.Node, reached map[string]bool, frontier bool) {
	count, bracket := groupHeaderCountAndBracket(g)

	// The group key is rendered OPAQUELY and is NEVER parsed: this renderer does
	// not split it, does not read a file or offset out of it, and does not depend
	// on how many components it has.
	fmt.Fprintf(sb, "\n- `%s` may %s one of %d candidates - %s [%s]\n",
		g.FromID, g.EdgeType, count, groupSemanticsText(g), bracket)

	for i := range g.Members {
		id := g.Members[i].ToId
		line := fmt.Sprintf("    %d. `%s`", i+1, id)
		if n, ok := nodes[id]; ok && n != nil {
			if n.FilePath != "" {
				line += fmt.Sprintf(" - %s:%d", n.FilePath, n.StartLine)
			}
			if n.Signature != "" {
				line += fmt.Sprintf(" - %s", n.Signature)
			}
		} else {
			line += " (unhydrated)"
		}
		if reached[id] {
			line += " (the node this walk reached)"
		}
		sb.WriteString(line)
		sb.WriteString("\n")
	}

	if frontier {
		sb.WriteString("    traversal stops at this candidate group - re-traverse from a validated candidate to continue\n")
	}
}

// groupHeaderCountAndBracket computes the header count and the bracket contents.
//
// THE COUNT IS THE DECLARED SIZE WHEN KNOWN AND THE OBSERVED SIZE WHEN NOT. A
// partial group keeps its DECLARED count and states how many are shown, so it
// never reads as a complete smaller group. When Declared is 0 the count falls
// back to the observed size — a group always has at least one member, so
// "one of 0 candidates" is unreachable.
//
// When Declared is 0 the bracket carries `declared count unknown` INSTEAD of a
// confidence figure: a zero or absent Confidence is precisely why Declared could
// not be recovered, so printing "confidence 0.00 each" would dress an unknown as
// a measurement.
//
// CONFIDENCE APPEARS ONCE, AT GROUP LEVEL — 1/N is derivable, so a per-candidate
// confidence would be per-line noise.
func groupHeaderCountAndBracket(g *CandidateGroup) (int, string) {
	observed := len(g.Members)
	if g.Declared <= 0 {
		return observed, fmt.Sprintf("group %s - declared count unknown", g.Key)
	}
	bracket := fmt.Sprintf("group %s - confidence %.2f each", g.Key, g.ConfidenceEach())
	if observed < g.Declared {
		bracket += fmt.Sprintf(" - %d of %d shown", observed, g.Declared)
	}
	return g.Declared, bracket
}

// groupSemanticsText spells the closed/open distinction. An ambiguous-name group
// is CLOSED: the reference means exactly one of these. A dynamic group is OPEN:
// dynamic dispatch can reach a target no static enumeration can see, so it must
// never be read as closed.
func groupSemanticsText(g *CandidateGroup) string {
	if g.Closed() {
		return "exactly one is the real target"
	}
	return "dispatches to one of these, or beyond"
}

// groupSemanticsKey is the machine-readable twin of groupSemanticsText, for the
// JSON surfaces' `semantics` key.
func groupSemanticsKey(g *CandidateGroup) string {
	if g.Closed() {
		return "exactly-one-of"
	}
	return "one-of-these-or-beyond"
}

// candidateGroupsJSON renders one row per group for the JSON surfaces, following
// edgeMetadataJSON's row idiom (map[string]any per row) so the emitters in this
// package stay consistent.
//
// `frontier` is always true — the uniform short-circuit means every group IS a
// frontier. It is emitted EXPLICITLY rather than left implicit so a JSON consumer
// never has to infer traversal semantics from the method tag.
func candidateGroupsJSON(groups []CandidateGroup, nodes map[string]*knowledgev1.Node, reached map[string]bool) []map[string]any {
	rows := make([]map[string]any, 0, len(groups))
	for i := range groups {
		g := &groups[i]
		candidates := make([]map[string]any, 0, len(g.Members))
		for j := range g.Members {
			id := g.Members[j].ToId
			cand := map[string]any{"id": id}
			if n, ok := nodes[id]; ok && n != nil {
				if n.FilePath != "" {
					cand["file"] = n.FilePath
					cand["line"] = n.StartLine
				}
				if n.Signature != "" {
					cand["signature"] = n.Signature
				}
			}
			if reached[id] {
				cand["reached"] = true
			}
			candidates = append(candidates, cand)
		}
		rows = append(rows, map[string]any{
			"from":                g.FromID,
			"edge_type":           g.EdgeType,
			"method":              g.Method,
			"semantics":           groupSemanticsKey(g),
			"group_key":           g.Key,
			"confidence_each":     g.ConfidenceEach(),
			"declared_candidates": g.Declared,
			"observed_candidates": len(g.Members),
			"complete":            g.Complete(),
			"frontier":            true,
			"candidates":          candidates,
		})
	}
	return rows
}

// traversalGroups is everything the two traverse arms need about groups, derived
// ONCE so text and JSON list exactly the same nodes and edges.
type traversalGroups struct {
	groups     []CandidateGroup
	results    []TraversalResult
	ungrouped  []knowledgev1.Edge
	reached    map[string]bool
	incomplete bool
}

// prepareTraversalGroups reconstructs the groups, applies the frontier
// short-circuit to BOTH the node list and the edge list, and folds the response's
// own truncation flag into the incompleteness signal.
//
// EVERY ADDED BEHAVIOR IS GATED ON A GROUP EXISTING. With no groups this returns
// the input results and the input edges untouched and incomplete=false whatever
// truncated says — which is what keeps an ordinary traversal byte-identical to
// today, and matters because code-graph walks now always request edge metadata
// and so can come back truncated where they never used to.
func prepareTraversalGroups(start string, results []TraversalResult, edges []knowledgev1.Edge, truncated bool) traversalGroups {
	groups, ungrouped := GroupCandidateEdges(edges)
	kept, reached, incomplete := FrontierFilter(start, results, edges, groups)
	if len(groups) > 0 {
		if truncated {
			incomplete = true
		}
		// Edges to nodes the frontier suppressed are withheld too, so the edges
		// section cannot show a path continuing past a group the node list says
		// the walk stopped at.
		ungrouped = FrontierEdges(start, ungrouped, reached)
	}
	return traversalGroups{groups: groups, results: kept, ungrouped: ungrouped, reached: reached, incomplete: incomplete}
}

// attachCandidateGroupsJSON adds the two group keys to a traversal JSON payload.
//
// BOTH KEYS ARE OMITTED ENTIRELY when no group exists, whatever the response's
// truncation flag says, so a zero-group payload stays byte-identical to today's.
// group_reconstruction_incomplete is emitted only as true, never as false: a
// claim of incompleteness on a payload with no reconstruction in it would
// describe something that never happened.
func attachCandidateGroupsJSON(payload map[string]any, groups []CandidateGroup, nodes map[string]*knowledgev1.Node, reached map[string]bool, incomplete bool) {
	if len(groups) == 0 {
		return
	}
	payload["edge_groups"] = candidateGroupsJSON(groups, nodes, reached)
	if incomplete {
		payload["group_reconstruction_incomplete"] = true
	}
}

// traversalNodeIndex indexes the walk's reached nodes by ID so a group block can
// render each candidate's file path and signature from the same response that
// carried the edges — no extra fetch.
func traversalNodeIndex(results []TraversalResult) map[string]*knowledgev1.Node {
	idx := make(map[string]*knowledgev1.Node, len(results))
	for _, r := range results {
		if r.Node != nil {
			idx[r.Node.Id] = r.Node
		}
	}
	return idx
}

// The reached set is NOT derived from the result list: FrontierFilter computes it
// as (reachable ∪ frontier), which is what makes the marker correct on a reverse
// walk where the group's source is only reachable across a group edge.
