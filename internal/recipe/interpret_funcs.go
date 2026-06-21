// SPDX-License-Identifier: Apache-2.0

package recipe

import (
	"fmt"
	"strings"
)

// hasEdge checks the in-memory sourceView for edges matching edgeType and
// direction on the current row's node. Returns "true" on the first match, ""
// otherwise. Unknown directions error out; the parser guards against bad
// directions for traverse but has_edge's string argument is only validated
// here.
func hasEdge(
	row *Row,
	edgeType, direction string,
	sv *sourceView,
) (string, error) {
	if row == nil || row.NodeID == "" {
		return "", nil
	}
	dir, err := parseDirection(direction)
	if err != nil {
		return "", err
	}
	if len(sv.edgesFrom(row.NodeID, edgeType, dir)) > 0 {
		return "true", nil
	}
	return "", nil
}

// childrenConcat walks outgoing edges of the given type from the
// current row's node, fetches each target node's named field, and
// returns the values joined by sep. The "outgoing" direction is
// implicit in the name (children = reachable downward); for the
// reverse aggregation use ancestorsConcat.
//
// Skips empty values silently — a target whose field is "" doesn't
// contribute a duplicate separator. Skips targets that fail to
// resolve (defensive against orphan edges in malformed sources).
//
// This is the load-bearing primitive for PDF-source recipes:
// section nodes carry only titles, while their CONTAINS-child
// paragraphs carry the body. children_concat lets the section's
// emitted pattern carry the section body in its description, which
// is what makes BM25 / vector search rank the pattern correctly.
func childrenConcat(
	row *Row,
	edgeType, field, sep string,
	sv *sourceView,
) string {
	if row == nil || row.NodeID == "" {
		return ""
	}
	return strings.Join(collectNeighborField(row, edgeType, outgoingEdges, field, sv), sep)
}

// hasAncestor walks incoming edges of edgeType transitively from the
// current row's node and returns "1" if any ancestor's named field
// matches the regex pattern, "" otherwise. Bounded depth prevents
// cycles in malformed source graphs from causing infinite walks.
//
// Use case: a recipe over a PDF wants to drop every section under
// "Part I" or "Foreword" without enumerating each leaf section by
// name. `filter not(has_ancestor("CONTAINS", "symbol_name",
// "^(Foreword|Preface)$"))` is the natural expression.
func hasAncestor(
	row *Row,
	edgeType, field, pattern string,
	sv *sourceView,
) (string, error) {
	if row == nil || row.NodeID == "" {
		return "", nil
	}
	re, err := compileRegex(pattern)
	if err != nil {
		return "", fmt.Errorf("has_ancestor: compile %q: %w", pattern, err)
	}
	const maxDepth = 32
	visited := map[string]bool{row.NodeID: true}
	frontier := []string{row.NodeID}
	for depth := 0; depth < maxDepth && len(frontier) > 0; depth++ {
		next := frontier[:0:0]
		for _, nodeID := range frontier {
			for _, ancestorID := range sv.edgesFrom(nodeID, edgeType, incomingEdges) {
				if visited[ancestorID] {
					continue
				}
				visited[ancestorID] = true
				node, ok := sv.nodeByID(ancestorID)
				if !ok {
					continue
				}
				val := readNodeField(node, []string{field})
				if re.MatchString(val) {
					return "1", nil
				}
				next = append(next, ancestorID)
			}
		}
		frontier = next
	}
	return "", nil
}

// ancestorsConcat is childrenConcat's incoming-direction sibling.
// Walks edges TO the current row (i.e. nodes whose outgoing edge of
// edgeType points at this row) and concatenates the named field.
//
// Use case: a target node referenced by many sources, gathering
// referrer names. Less common than children_concat; included for
// symmetry so recipes that want the reverse traversal don't have to
// invert the source graph manually.
func ancestorsConcat(
	row *Row,
	edgeType, field, sep string,
	sv *sourceView,
) string {
	if row == nil || row.NodeID == "" {
		return ""
	}
	return strings.Join(collectNeighborField(row, edgeType, incomingEdges, field, sv), sep)
}

// collectNeighborField is the shared body of children/ancestors
// aggregation. Walks edges of edgeType in dir, resolves each neighbor
// against the in-memory sourceView, reads the named field, and returns
// the non-empty values in edge-iteration order. Orphan edges (target
// node missing) are skipped silently — recipe authors don't need to
// debug source-graph inconsistencies, so the function returns []string
// only.
func collectNeighborField(
	row *Row,
	edgeType string,
	dir edgeDirection,
	field string,
	sv *sourceView,
) []string {
	var out []string
	for _, neighborID := range sv.edgesFrom(row.NodeID, edgeType, dir) {
		node, ok := sv.nodeByID(neighborID)
		if !ok {
			continue
		}
		val := readNodeField(node, []string{field})
		if val != "" {
			out = append(out, val)
		}
	}
	return out
}

// parseDirection maps the recipe string to an edgeDirection.
func parseDirection(d string) (edgeDirection, error) {
	switch strings.ToLower(d) {
	case "in":
		return incomingEdges, nil
	case "out":
		return outgoingEdges, nil
	case "both":
		return bothEdges, nil
	}
	return 0, fmt.Errorf("edge direction must be in|out|both, got %q", d)
}
