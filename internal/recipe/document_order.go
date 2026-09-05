// SPDX-License-Identifier: Apache-2.0

package recipe

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// document_order.go — the per-run READING-ORDER INDEX every rowset is sorted
// through, and the shared sibling comparator it is built from.
//
// WHY ONE INDEX RATHER THAN A PER-PARENT SORT. A sibling index orders children
// under ONE parent; a document position orders every node against every other.
// Two paragraphs sitting at sibling index 1 under different sections are
// incomparable to a sibling sort and strictly ordered in a document — so the
// only construction that gives `select`, `traverse` and `walk` the SAME stated
// guarantee is one depth-first preorder walk of the whole positioned relation,
// assigning each node a rank in reading order.
//
// THE CONTAINMENT RELATION IS POSITION-DERIVED, NOT EDGE-TYPE-NAMED. `select`
// names no edge type, so this index cannot take one, and hardcoding a contains
// spelling into a graph-agnostic package would be a second definition of
// containment. positionedChildEdges admits every out-edge that yields an order
// key, whatever its type.
//
// THE OPERATIVE RULE IS ABOUT THE TARGET NODE, NOT THE EDGE, and stating it the
// other way round is how this comment was wrong before. An edge is in the
// document relation exactly when its TARGET NODE carries a parseable position,
// or failing that when the edge's own Evidence does — node first, edge second,
// which is the precedence childOrderKey implements. On the two collectors a
// recipe can run against, only CONTAINS edges reach a positioned target, and
// that is checked rather than assumed. The full edge census. FOUR families are
// drawn by the two collectors themselves:
//   - pdf CONTAINS         — positioned on both carriers; the document relation.
//   - web CONTAINS         — the same.
//   - web REFERENCES       — terminates on a PAGE node only when
//                            resolveInternalLinks resolved the link. An external
//                            cite, and an internal link the crawl never visited
//                            and so downgraded to rel=external, both keep the
//                            `web:url:<absolute>` placeholder that no emitter
//                            ever creates a node for. Neither shape yields a
//                            key: a page node carries no position, an absent
//                            node is never read at all, and emitLinks builds the
//                            Evidence on both as rel and url only.
//   - github materializer  — its lowercase contains family carries no Evidence
//                            at all, and its REFERENCES edge to a
//                            materialization-skipped warning node carries rel,
//                            url and materialization_skipped, no position, onto
//                            a synthetic document node that has none either.
//
// A FIFTH PRODUCER WRITES INTO THE SAME GRAPH, and it is not a family but a whole
// graph: a materialize_github run appends parser.PopulateForExternalGraph's ENTIRE
// node and edge output to the batch a recipe later loads. That is every family the
// repo parser draws — uppercase file-to-symbol CONTAINS, IMPORTS, LANGUAGE,
// IMPLEMENTS, CALLS, USES_TYPE and the FLOWS_TO and METHOD_* families — and listing
// them here would rot the first time that parser gains one. The closure is stated
// as a PACKAGE-WIDE property instead, which is both shorter and stronger: the
// parser package writes no `position` key on any node metadata map or any edge
// Evidence, so none of its nodes can be a positioned target and none of its edges
// can yield an order key, whatever family it belongs to. The materializer does not
// reintroduce one on the way through either — enrichForRecipes stamps uri,
// file_path, relpath, language and repo, and no position.
//
// SO THE INVARIANT IS A PROPERTY OF TODAY'S COLLECTORS RATHER THAN OF THIS CODE.
// A collector that stamps a position on a node reachable by a SECOND edge of any
// type gives that node two positioned parents, and this index will refuse the
// whole graph — correctly by its own rule, and confusingly, because the refusal
// speaks of containment edges. TestDocumentOrder_RefusesAmbiguousPosition pins
// that behaviour with a cross-reference edge so it is a decided property rather
// than an accident waiting to be discovered.

// documentOrder is one run's reading-order index: a node id to its rank in a
// depth-first preorder walk of the positioned relation, 0 for the first node
// of the document.
//
// A node touched by NO positioned edge is deliberately ABSENT from rank rather
// than ranked late. Absence is what lets sortRowsByDocumentOrder send it to the
// by-node-id tail after every ranked node, which is the guarantee evalSelect
// and evalTraverse publish.
type documentOrder struct {
	rank map[string]int
}

// childOrderKey resolves one edge's order key, NODE FIRST AND EDGE SECOND.
//
// The child node's own `position` metadata wins when it parses as an integer;
// otherwise the key falls back to the position on the EDGE's Evidence, which
// positionFromEvidence reports as a soft miss when it is absent or malformed.
// On a graph collected by either raw collector both carriers hold the same
// value, so the precedence is observable only where they disagree or one is
// missing — which is precisely the case a hand-built or partially-migrated
// graph presents.
func (sv *sourceView) childOrderKey(e *knowledgev1.Edge) (int, bool) {
	if e == nil {
		return 0, false
	}
	if child, ok := sv.byID[e.ToId]; ok {
		if pos, err := strconv.Atoi(kgtypes.Value(child, "position")); err == nil {
			return pos, true
		}
	}
	return positionFromEvidence(e.Evidence)
}

// sortEdgesByOrderKey is THE sibling comparator, shared by childEdgesOrdered and
// by this index so the two cannot order the same children differently.
//
// ORDERING RULE: ascending by order key; an edge yielding no key sorts AFTER
// every keyed edge and keeps materialization order among its unkeyed peers. The
// sort is stable, so a fixed source graph renders in a fixed order across runs —
// which matters because extract output is read by people comparing one run
// against another.
func (sv *sourceView) sortEdgesByOrderKey(out []*knowledgev1.Edge) {
	sort.SliceStable(out, func(i, j int) bool {
		pi, oki := sv.childOrderKey(out[i])
		pj, okj := sv.childOrderKey(out[j])
		if oki != okj {
			return oki // a positioned edge precedes an unpositioned one
		}
		if !oki {
			return false // both unpositioned: stable sort keeps their order
		}
		return pi < pj
	})
}

// positionedChildEdges returns the out-edges of id that carry an order key, in
// that key's order. Edge TYPE is not consulted: an edge is part of the document
// relation exactly when a position can be read for it.
func (sv *sourceView) positionedChildEdges(id string) []*knowledgev1.Edge {
	all := sv.outEdges[id]
	out := make([]*knowledgev1.Edge, 0, len(all))
	// IDENTICAL CLAIMS ARE COLLAPSED HERE TOO, not only in the refusal: two edges
	// with the same target AND the same key are one claim, and admitting both
	// would walk the child's whole subtree twice for no additional information.
	// By the time this runs the ambiguity pre-pass has already refused every
	// graph where two claims on one child DISAGREE, so anything collapsed here is
	// a true duplicate.
	seen := map[childClaim]bool{}
	for _, e := range all {
		key, ok := sv.childOrderKey(e)
		if !ok {
			continue
		}
		claim := childClaim{child: e.ToId, position: key}
		if seen[claim] {
			continue
		}
		seen[claim] = true
		out = append(out, e)
	}
	sv.sortEdgesByOrderKey(out)
	return out
}

// documentOrderIndex builds the reading-order index once per run and memoizes it
// on the view, in TWO PASSES whose first pass is a REFUSAL.
//
// PASS ONE walks every out-edge once, recording per child every parent claiming
// it through a positioned edge, and per parent whether it claims anyone. A
// target absent from byID is skipped — the same orphan guard evalTraverse
// applies. refuseAmbiguousPositions then errors on a node with more than one
// positioned parent.
//
// PASS TWO walks depth-first preorder from every root, ascending, assigning
// ranks 0,1,2,… A ROOT PARENTS AT LEAST ONE POSITIONED EDGE AND IS THE CHILD OF
// NONE. A node touched by no positioned edge is NOT a root: it has no document
// position at all, so it is left unranked rather than interleaved among the real
// roots by id.
//
// THERE IS NO VISITED GUARD HERE, and the reason is a proof rather than a
// preference. A cycle in the POSITIONED relation is reachable from a root only
// through a node the cycle re-enters from outside it, and such a node has two
// positioned parents — so pass one refuses every reachable cycle before pass two
// starts. THE WALK RULE KEEPS ITS OWN GUARD for a different relation: it
// enumerates every edge of its named type, positioned or not, so a cycle of
// UNPOSITIONED edges never reaches this refusal. Do not delete that one by
// analogy with this one.
//
// Cost is O(V+E), paid once per run and memoized; every ordering consumer then
// pays one map lookup per comparison. It is built lazily rather than in
// loadSourceView because this package's tests construct sourceView literals
// directly, and an eager build would leave every such fixture with a nil index.
func (sv *sourceView) documentOrderIndex() (*documentOrder, error) {
	if sv.docOrder != nil {
		return sv.docOrder, nil
	}
	claims := map[string][]positionedClaim{}
	parenting := map[string]bool{}
	for from, edges := range sv.outEdges {
		for _, e := range edges {
			key, ok := sv.childOrderKey(e)
			if !ok {
				continue
			}
			if _, ok := sv.byID[e.ToId]; !ok {
				continue
			}
			claims[e.ToId] = append(claims[e.ToId], positionedClaim{parent: from, position: key})
			parenting[from] = true
		}
	}
	if err := refuseAmbiguousPositions(claims); err != nil {
		return nil, err
	}
	roots := make([]string, 0, len(parenting))
	for id := range parenting {
		if len(claims[id]) == 0 {
			roots = append(roots, id)
		}
	}
	sort.Strings(roots)

	idx := &documentOrder{rank: make(map[string]int, len(sv.byID))}
	next := 0
	var descend func(id string)
	descend = func(id string) {
		idx.rank[id] = next
		next++
		for _, e := range sv.positionedChildEdges(id) {
			if _, ok := sv.byID[e.ToId]; !ok {
				continue
			}
			descend(e.ToId)
		}
	}
	for _, root := range roots {
		descend(root)
	}
	sv.docOrder = idx
	return idx, nil
}

// positionedClaim is one positioned edge into a child: which parent drew it, and
// at which position. The POSITION is carried because ambiguity is a property of
// the claims a node receives, not of how many edges carry them — see below.
type positionedClaim struct {
	parent   string
	position int
}

// childClaim keys the duplicate collapse in positionedChildEdges, where the
// varying end is the CHILD rather than the parent: one parent's out-edges are
// being deduped, so what distinguishes two of them is which child they reach and
// at what position. It is a separate type from positionedClaim because reusing
// that one would have put a child id in a field named parent.
type childClaim struct {
	child    string
	position int
}

// refuseAmbiguousPositions errors on the first node — first in sorted id order,
// so the message is identical on every run over the same graph — that receives
// more than one DISTINCT positioned claim.
//
// AMBIGUITY IS REFUSED, NOT RESOLVED. A node with two document positions has no
// way to choose between them, so ranking it under whichever parent the walk
// reached first would answer a question the data does not answer, with an answer
// that depends on Go's map iteration order. Refusing is the standing
// bad-input-errors rule, and it is also what makes every cycle in the positioned
// relation terminate.
//
// A TRUE DUPLICATE IS NOT AMBIGUOUS, and this is why the claim carries its
// position. Two edges identical in parent, child AND position say the same thing
// twice: there is one document position and nothing to choose. Counting
// positioned EDGES reported that as `claimed by 2 positioned parents ("p", "p")`
// and refused the run with a repair instruction naming an edge that is not a
// second parent. Identical claims are deduped; two claims from ONE parent at
// DIFFERENT positions are still ambiguous and still refuse, with a message that
// says what actually happened rather than inventing a second parent.
func refuseAmbiguousPositions(claims map[string][]positionedClaim) error {
	ambiguous := make([]string, 0)
	distinct := map[string][]positionedClaim{}
	for child, received := range claims {
		seen := map[positionedClaim]bool{}
		kept := make([]positionedClaim, 0, len(received))
		for _, c := range received {
			if seen[c] {
				continue
			}
			seen[c] = true
			kept = append(kept, c)
		}
		distinct[child] = kept
		if len(kept) > 1 {
			ambiguous = append(ambiguous, child)
		}
	}
	if len(ambiguous) == 0 {
		return nil
	}
	sort.Strings(ambiguous)
	child := ambiguous[0]
	return fmt.Errorf("recipe: document order is ambiguous: %s. The run was refused rather than "+
		"ordered under whichever claim was reached first. Remove the extra positioned containment "+
		"edge, or re-collect the source graph", describeAmbiguity(child, distinct[child]))
}

// describeAmbiguity renders the offending claims, distinguishing the two shapes
// because their repairs differ: several parents claiming one child, versus one
// parent claiming it at several positions.
func describeAmbiguity(child string, kept []positionedClaim) string {
	parents := map[string]bool{}
	for _, c := range kept {
		parents[c.parent] = true
	}
	if len(parents) == 1 {
		// SORTED AS NUMBERS, NOT AS TEXT. A string sort renders three edges at
		// positions 2, 3 and 10 as "10, 2, 3", which reads as a different set of
		// positions than the graph actually carries.
		ordered := make([]int, 0, len(kept))
		for _, c := range kept {
			ordered = append(ordered, c.position)
		}
		sort.Ints(ordered)
		positions := make([]string, 0, len(ordered))
		for _, p := range ordered {
			positions = append(positions, strconv.Itoa(p))
		}
		// THE COUNT IS len(kept) RATHER THAN THE LITERAL "two", because one parent
		// can claim a child at three positions as easily as at two — and the NOUN
		// beside it is "distinct positions" rather than "positioned edges" for the
		// same reason the dedupe above exists. len(kept) counts DISTINCT CLAIMS, so
		// a parent drawing two edges at position 3 and one at 10 has three edges and
		// two distinct positions; calling that "2 positioned edges" would be the
		// same small untruth as the hardcoded "two" this replaced.
		return fmt.Sprintf("node %q is claimed at %d distinct positions by one parent %q "+
			"with positions %s, so it has no single position in the document",
			child, len(kept), kept[0].parent, joinPlain(positions))
	}
	named := make([]string, 0, len(parents))
	for p := range parents {
		named = append(named, fmt.Sprintf("%q", p))
	}
	sort.Strings(named)
	return fmt.Sprintf("node %q is claimed by %d positioned parents (%s), so it has no single "+
		"position in the document", child, len(named), strings.Join(named, ", "))
}

// sortRowsByDocumentOrder sorts a rowset into document reading order in place,
// returning the index's refusal when the source graph carries an ambiguous
// position.
//
// A node for which no position is determinable follows EVERY ordered node, by
// node id — the tail is sorted rather than left in materialization order so a
// rowset of entirely unpositioned nodes is still stable across runs.
func sortRowsByDocumentOrder(rows []Row, sv *sourceView) error {
	if sv == nil {
		return nil
	}
	idx, err := sv.documentOrderIndex()
	if err != nil {
		return err
	}
	sort.SliceStable(rows, func(i, j int) bool {
		ri, oki := idx.rank[rows[i].NodeID]
		rj, okj := idx.rank[rows[j].NodeID]
		if oki != okj {
			return oki // a ranked node precedes an unranked one
		}
		if !oki {
			return rows[i].NodeID < rows[j].NodeID
		}
		return ri < rj
	})
	return nil
}
