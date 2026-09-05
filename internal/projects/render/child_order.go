// SPDX-License-Identifier: Apache-2.0

package render

import (
	"encoding/json"
	"sort"
	"strconv"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// child_order.go holds the positioned-sibling order key BuildChildIndex applies.
//
// THE RULES ARE CARRIED FROM THE RAW COLLECTORS' READING-ORDER INDEX, not
// invented here: a child's position rides TWO carriers with one meaning — the
// `position` key on the child NODE's metadata and a {"position":"N"} JSON blob
// on the containment EDGE's Evidence — and the NODE carrier wins when they
// disagree. Both collectors stamp both, so the precedence is observable only
// where they diverge or one is missing, which is exactly what a hand-built or
// partially-migrated graph presents. The reader that owns those rules for the
// raw graphs is unexported and package-local to the recipe package, so the rules
// are reimplemented here rather than called.
//
// ONE RULE OF THAT READER IS DELIBERATELY NOT CARRIED. Its own comment says
// "Edge TYPE is not consulted: an edge is part of the document relation exactly
// when a position can be read for it." That is safe there and WRONG here. Every
// route into BuildChildIndex passes ONE edge type to the traversal, so the edge
// set arrives already filtered to the containment relation; building the order
// key over unfiltered out-edges instead would make a plan_annotation's
// relates-to edge into the positioned PARENT of the section it annotates, since
// a section node carries a `position` key of its own. The CALLER'S TYPE FILTER
// is what defines the relation on this path; the order key only ranks within it.

// containsPositionEvidence is the JSON payload a positioned containment edge
// carries. Only the position is read; the schema is parsed field-by-field so
// additive fields do not break older readers.
type containsPositionEvidence struct {
	// Position is the child's index under its parent, stamped as a string by the
	// raw collectors and by the plan section builder.
	Position string `json:"position"`
}

// positionFromEdgeEvidence extracts a child's position from a containment edge's
// Evidence blob, reporting whether one was found.
//
// IT NEVER RETURNS AN ERROR AND NEVER COERCES. An absent, non-JSON, non-integer
// or empty position is a property of the graph rather than a caller mistake, so
// it is a soft miss reported as ok=false. Reading an unparseable carrier as
// position 0 would hoist that child to the FRONT of its parent's children — the
// one failure mode that turns a malformed key into a silently reordered tree.
func positionFromEdgeEvidence(evidence string) (int, bool) {
	if evidence == "" {
		return 0, false
	}
	var e containsPositionEvidence
	if err := json.Unmarshal([]byte(evidence), &e); err != nil {
		return 0, false
	}
	pos, err := strconv.Atoi(e.Position)
	if err != nil {
		return 0, false
	}
	return pos, true
}

// childOrderKey resolves one containment edge's order key, NODE FIRST AND EDGE
// SECOND, against the id→node lookup the index has already built.
//
// A soft miss (ok=false) means "this edge carries no position", which the
// comparator ranks after every positioned edge rather than at zero.
func childOrderKey(byID map[string]*knowledgev1.Node, e *knowledgev1.Edge) (int, bool) {
	if e == nil {
		return 0, false
	}
	if child, ok := byID[e.ToId]; ok {
		if pos, err := strconv.Atoi(kgtypes.Value(child, "position")); err == nil {
			return pos, true
		}
	}
	return positionFromEdgeEvidence(e.Evidence)
}

// sortStructureEdgesByPosition returns the edges ordered by their child's
// position: ascending by key, an edge yielding NO key sorting after every keyed
// edge and keeping its arrival order among its unkeyed peers.
//
// THE SORT IS STABLE AND AN UNKEYED EDGE RANKS EQUAL TO ITS UNKEYED PEERS, which
// is what makes this a no-op on every edge set that carries no position — every
// phase, step and criterion written before positions existed, and every tree the
// status cascade partitions. That property is the reason ordering lives in
// BuildChildIndex at all rather than in one renderer: the plan_tree json path,
// the json assemble, the ticket and project renderers and the status-cascade
// partitioner all take their child order from here without calling any tree
// renderer, and a comparator that reordered an unpositioned set would move every
// one of them.
//
// IT COPIES RATHER THAN SORTING IN PLACE. The slice belongs to the caller — it is
// the traversal's own result, and plan_tree reads it a second time for its json
// branch — so reordering it here would be a mutation of a value the caller still
// owns and still reads.
func sortStructureEdgesByPosition(byID map[string]*knowledgev1.Node, edges []*knowledgev1.Edge) []*knowledgev1.Edge {
	ordered := make([]*knowledgev1.Edge, len(edges))
	copy(ordered, edges)
	sort.SliceStable(ordered, func(i, j int) bool {
		pi, oki := childOrderKey(byID, ordered[i])
		pj, okj := childOrderKey(byID, ordered[j])
		if oki != okj {
			return oki // a positioned edge precedes an unpositioned one
		}
		if !oki {
			return false // both unpositioned: the stable sort keeps their order
		}
		return pi < pj
	})
	return ordered
}
