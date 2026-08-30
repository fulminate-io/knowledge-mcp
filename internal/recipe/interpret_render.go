// SPDX-License-Identifier: Apache-2.0

package recipe

import (
	"fmt"
	"strconv"
	"strings"
)

// interpret_render.go holds the two RENDER builtins — the ones that turn a
// document's shape into text rather than reading a single field.
//
// They live in their own file because the expression evaluator is already close
// to this package's file-length ceiling and these are two whole graph walks.
//
// Together with the virtual body field they are the canned section render for
// BOTH raw collectors from one recipe: subtree_concat over the contains edge
// with the body field walks a section's spine and reads whichever field that
// collector put the text in.

// maxRenderDepth bounds both walks below, mirroring the ancestor check's own
// bound. It is a guard against a malformed source graph, not a tuning knob: a
// document nested deeper than this is not a document.
const maxRenderDepth = 32

// evalRenderFunc handles the render builtins, returning the same
// (value, handled, err) triple as the string, graph and boolean category
// helpers so evalFunc can try each in turn.
func evalRenderFunc(row *Row, name string, args []string, sv *sourceView) (string, bool, error) {
	switch name {
	case "heading_path":
		if err := checkArity("heading_path", args, 3); err != nil {
			return "", true, err
		}
		return headingPath(row, args[0], args[1], args[2], sv), true, nil
	case "subtree_concat":
		if err := checkArity("subtree_concat", args, 4); err != nil {
			return "", true, err
		}
		v, err := subtreeConcat(row, args[0], args[1], args[2], args[3], sv)
		return v, true, err
	}
	return "", false, nil
}

// headingPath walks upward from the current row along incoming edges of
// edgeType, reads field at each ancestor, and joins the values ROOT-FIRST.
//
// The row's OWN field is not included: the row is the leaf, and its path is
// what the caller asked for. Empty values are skipped, matching the neighbor
// aggregation. Bounded by maxRenderDepth with a visited set, so a cycle in the
// source graph terminates instead of hanging.
//
// MULTI-PARENT RULE: at each hop it follows the FIRST incoming edge of
// edgeType in materialization order and stops there. Both raw collectors attach
// each node to exactly one parent, so a second parent means the source graph is
// malformed; picking the first is deterministic and stated rather than silently
// arbitrary. edgeType comes from the caller, so the single-parent assumption is
// a property of how a recipe uses this, not something it can enforce.
func headingPath(row *Row, edgeType, field, sep string, sv *sourceView) string {
	if row == nil || row.NodeID == "" {
		return ""
	}
	var upward []string
	visited := map[string]bool{row.NodeID: true}
	current := row.NodeID
	for range maxRenderDepth {
		parents := sv.edgesFrom(current, edgeType, incomingEdges)
		if len(parents) == 0 {
			break
		}
		parentID := parents[0]
		if visited[parentID] {
			break
		}
		visited[parentID] = true
		node, ok := sv.nodeByID(parentID)
		if !ok {
			break
		}
		if val := readNodeField(node, []string{field}); val != "" {
			upward = append(upward, val)
		}
		current = parentID
	}
	// The walk collected leaf-ward to root-ward; the caller asked for a path.
	for i, j := 0, len(upward)-1; i < j; i, j = i+1, j-1 {
		upward[i], upward[j] = upward[j], upward[i]
	}
	return strings.Join(upward, sep)
}

// subtreeConcat walks DOWNWARD from the current row through edges of edgeType,
// visiting each level's children in document order, and joins the non-empty
// field values with sep.
//
// maxDepth is a string integer, parsed the same way the slice builtin parses
// its bounds and erroring in the same shape on a non-integer. A depth of one
// yields the ordered immediate children; a larger depth renders the whole
// subtree.
//
// THE DEPTH IS THE POINT for a section render. The page-body flattener the web
// collector runs deliberately drops code blocks, tables, images and quotes, so
// a page's own description never contains them. This walk visits every child
// node type, which is why a section rendered through it carries the code the
// flattened body left out.
//
// A depth of zero or less returns "". Cycles terminate via a visited set.
func subtreeConcat(row *Row, edgeType, field, sep, maxDepthStr string, sv *sourceView) (string, error) {
	maxDepth, err := strconv.Atoi(maxDepthStr)
	if err != nil {
		return "", fmt.Errorf("subtree_concat: max_depth must be an integer, got %q", maxDepthStr)
	}
	if row == nil || row.NodeID == "" || maxDepth <= 0 {
		return "", nil
	}
	if maxDepth > maxRenderDepth {
		maxDepth = maxRenderDepth
	}
	var values []string
	visited := map[string]bool{row.NodeID: true}
	var walk func(id string, depth int)
	walk = func(id string, depth int) {
		if depth > maxDepth {
			return
		}
		for _, e := range sv.childEdgesOrdered(id, edgeType) {
			childID := e.ToId
			if visited[childID] {
				continue
			}
			visited[childID] = true
			node, ok := sv.nodeByID(childID)
			if !ok {
				continue
			}
			if val := readNodeField(node, []string{field}); val != "" {
				values = append(values, val)
			}
			walk(childID, depth+1)
		}
	}
	walk(row.NodeID, 1)
	return strings.Join(values, sep), nil
}
