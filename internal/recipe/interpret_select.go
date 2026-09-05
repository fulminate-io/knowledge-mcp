// SPDX-License-Identifier: Apache-2.0

package recipe

import (
	"context"
	"sort"
	"strconv"
	"strings"
)

// evalSelect replaces env.Rows with the set of nodes of the given
// type matching the optional where clause. Reads from the in-memory
// sourceView's by-type index (the materialized source graph already
// excluded tombstones at load time). Empty selections are valid —
// subsequent rules short-circuit naturally on an empty row set.
//
// THE ROWSET IS IN DOCUMENT READING ORDER, and a node for which no position is
// determinable follows EVERY ordered node, by node id. That second half is true
// only because the reading-order index leaves a node touched by no positioned
// edge UNRANKED rather than rooting it (document_order.go); if that construction
// changes, this sentence, the identical one on evalTraverse and the two
// published copies in the recipes help and guide change with it.
//
// THE INDEX'S REFUSAL IS PROPAGATED, never swallowed. A source graph carrying an
// ambiguous position is refused, and a call site that dropped that error would
// re-introduce the coercion on its own path while every other path refused —
// worse than never refusing, because the behaviour would then depend on which
// rule the recipe happened to use.
func evalSelect(ctx context.Context, env *Env, r RuleSelect, sv *sourceView) error {
	nodes := sv.nodesByType(r.NodeType)
	rows := make([]Row, 0, len(nodes))
	for _, n := range nodes {
		rows = append(rows, Row{NodeID: n.Id, Node: n, Vars: map[string]string{}})
	}
	if r.Where != nil {
		kept := rows[:0]
		for i := range rows {
			ok, err := evalWhereTree(ctx, env, &rows[i], r.Where, sv)
			if err != nil {
				return err
			}
			if ok {
				kept = append(kept, rows[i])
			}
		}
		rows = kept
	}
	if err := sortRowsByDocumentOrder(rows, sv); err != nil {
		return err
	}
	env.Rows = rows
	return nil
}

// evalTraverse walks the given edge type from each current row,
// replacing the selection with the traversed set. Each new row
// carries a pointer (via Vars) back to the var the recipe wrote the
// `as` clause for, so subsequent expressions can resolve $target /
// $other against the traversed node ID.
//
// Implementation note: we keep per-row Vars of the SOURCE row and
// stamp the traverse target's ID as the new row's NodeID. The var
// name under `as $var` becomes a Vars entry whose value is the
// traverse-target node ID, which is the same as Row.NodeID for the
// new row but is kept under the var key so later rules can reference
// the same ID via $var.
//
// THE ROWSET IS IN DOCUMENT READING ORDER, and a node for which no position is
// determinable follows EVERY ordered node, by node id — the same guarantee
// evalSelect publishes, in the same words, and the index's refusal is propagated
// here too.
//
// WHY THE FINISHED ROWSET IS SORTED RATHER THAN THE PER-ROW EXPANSION ROUTED
// THROUGH childEdgesOrdered: ordered child edges give the same answer only for
// an `out` traverse over a tree. `in` and `both` have no per-parent order to
// inherit. Sorting the finished rowset through the one index gives all three
// directions the same stated guarantee, and for `out` it is identical to the
// per-parent walk.
func evalTraverse(_ context.Context, env *Env, r RuleTraverse, sv *sourceView) error {
	dir, err := parseDirection(r.Direction)
	if err != nil {
		return err
	}
	out := make([]Row, 0, len(env.Rows))
	for i := range env.Rows {
		prev := &env.Rows[i]
		// edgesAlong rather than edgesFrom: the row this walk builds carries the
		// edge it was reached along, so `edge.…` can read that edge's type and
		// the Evidence keys both raw collectors stamp on a contains edge.
		for _, hop := range sv.edgesAlong(prev.NodeID, r.EdgeType, dir) {
			// Resolve the target node for field access.
			target, ok := sv.nodeByID(hop.NodeID)
			if !ok {
				// Skip orphan edges whose target isn't in the graph —
				// same defensive behavior as existing traversal utilities.
				continue
			}
			vars := cloneRowVars(prev.Vars)
			if r.As != "" {
				vars[r.As] = hop.NodeID
			}
			out = append(out, Row{NodeID: hop.NodeID, Node: target, Vars: vars, Edge: hop.Edge})
		}
	}
	if err := sortRowsByDocumentOrder(out, sv); err != nil {
		return err
	}
	env.Rows = out
	return nil
}

// evalWalk replaces the rowset with each current row's SUBTREE along r.EdgeType,
// in document reading order, stamping the level on every row.
//
// THE STARTING ROW IS NOT EMITTED. Depth 1 is a direct child, mirroring
// traverse's replace-the-rowset semantics: a walk answers "what is under this",
// not "this and what is under it".
//
// THE VISITED SET IS SCOPED PER WALK ROOT, NOT PER RULE. It is created and
// seeded fresh for each starting row, so a node reachable from two starting rows
// is visited ONCE PER ROOT and appears once under each — which makes a walk from
// several rows the CONCATENATION of their subtrees, not a set union. A node
// under two starting rows yields TWO rows, and they land adjacent because both
// carry its one document rank; MEASURED on a three-level document whose sections
// nest, `select section` then `walk CONTAINS` returns [s1 p p]. The alternative,
// a rule-scoped set, would silently drop the second root's copy, and which copy
// was dropped would move when the rowset moved. Within one root the set is still
// a cycle guard, and it is the only thing standing between an unpositioned cycle
// and an unbounded descent.
//
// THAT GUARD IS KEPT EVEN THOUGH THE READING-ORDER INDEX RETIRED ITS OWN. The
// index has no guard because its ambiguity pre-pass makes every cycle in the
// POSITIONED relation unreachable; this rule enumerates every edge of its NAMED
// TYPE, positioned or not, so a cycle of unpositioned edges never reaches that
// refusal. Do not delete this one by analogy with that one.
//
// EVERY WALKED ROW CARRIES THE EDGE IT WAS REACHED ALONG, in Row.Edge, exactly
// as a traversed row does. A walk is an edge step: each row below is reached
// along exactly one edge of the named type, so `edge.type` and the Evidence keys
// both raw collectors stamp — `edge.position` above all — read on a walked row
// the way they read on a traversed one. childEdgesOrdered already yields the
// edge rather than a neighbor id, so this costs nothing; leaving it unset made
// an `edge.…` read on a walked row answer empty instead of answering, which is
// the silent zero this interpreter refuses everywhere else.
//
// The rows are ordered by sorting the finished rowset through the one index,
// which makes a walk from several starting rows ONE reading order and keeps the
// index's ambiguity refusal armed on this path too.
func evalWalk(_ context.Context, env *Env, r RuleWalk, sv *sourceView) error {
	out := make([]Row, 0, len(env.Rows))
	for i := range env.Rows {
		prev := &env.Rows[i]
		visited := map[string]bool{prev.NodeID: true}
		var descend func(id string, depth int)
		descend = func(id string, depth int) {
			for _, e := range sv.childEdgesOrdered(id, r.EdgeType) {
				target, found := sv.nodeByID(e.ToId)
				if !found {
					// Skip orphan edges whose target isn't in the graph — the
					// same guard evalTraverse applies.
					continue
				}
				if visited[e.ToId] {
					continue
				}
				visited[e.ToId] = true

				vars := cloneRowVars(prev.Vars)
				vars["walk.depth"] = strconv.Itoa(depth)
				vars["walk.position"] = ""
				if key, ok := sv.childOrderKey(e); ok {
					vars["walk.position"] = strconv.Itoa(key)
				}
				if r.As != "" {
					vars[r.As] = e.ToId
				}
				out = append(out, Row{NodeID: e.ToId, Node: target, Vars: vars, Edge: e})
				descend(e.ToId, depth+1)
			}
		}
		descend(prev.NodeID, 1)
	}
	if err := sortRowsByDocumentOrder(out, sv); err != nil {
		return err
	}
	env.Rows = out
	return nil
}

// evalFilter drops every row whose where-tree evaluates false.
//
// The in-place `env.Rows[:0]` reuse is load-bearing rather than incidental: it
// avoids a second row-slice allocation per filter rule, which on a 795-node
// graph is roughly 32KB per rule per run.
func evalFilter(ctx context.Context, env *Env, r RuleFilter, sv *sourceView) error {
	kept := env.Rows[:0]
	for i := range env.Rows {
		ok, err := evalWhereTree(ctx, env, &env.Rows[i], r.Where, sv)
		if err != nil {
			return err
		}
		if ok {
			kept = append(kept, env.Rows[i])
		}
	}
	env.Rows = kept
	return nil
}

// evalBind evaluates Value once per current row and stores the per-row
// result under Var in that row's Vars. env.Vars receives the LAST
// per-row value as an audit trail for downstream rules that run after
// the rowset drains (e.g. a final emit that no longer sees rows).
//
// Per-row evaluation matters because the canonical bind use-case
// (`bind $slug := lower(concat(page.name, "-ext"))`) reads page.X off
// the current row. A "compute on row 0, broadcast" implementation
// would silently overwrite every row's $slug with row-0's value,
// turning a per-row derivation into a one-row-wins bug — which is
// what an earlier draft of this function did and what produced the
// "every main() pattern named after the first file" miscompile in
// the cox-buday v3 recipe iteration.
//
// When env.Rows is empty (e.g. a bind appearing before any select)
// the expression is evaluated against a nil row so literal-only
// expressions still work; the result lands only in env.Vars.
func evalBind(ctx context.Context, env *Env, r RuleBind, sv *sourceView) error {
	if len(env.Rows) == 0 {
		v, err := evalExpr(ctx, env, nil, r.Value, sv)
		if err != nil {
			return err
		}
		env.Vars[r.Var] = v
		return nil
	}
	var lastValue string
	for i := range env.Rows {
		v, err := evalExpr(ctx, env, &env.Rows[i], r.Value, sv)
		if err != nil {
			return err
		}
		if env.Rows[i].Vars == nil {
			env.Rows[i].Vars = map[string]string{}
		}
		env.Rows[i].Vars[r.Var] = v
		lastValue = v
	}
	env.Vars[r.Var] = lastValue
	return nil
}

// evalGroupBy collapses rows with the same key into one representative
// row per key. The representative row preserves the first row's
// NodeID and Node; the `group.keys` pseudo-var on that row carries
// the comma-joined list of distinct key values observed (in sorted
// order so output is deterministic).
func evalGroupBy(ctx context.Context, env *Env, r RuleGroupBy, sv *sourceView) error {
	groups := map[string][]int{}
	keysInOrder := []string{}
	for i := range env.Rows {
		k, err := evalExpr(ctx, env, &env.Rows[i], r.Key, sv)
		if err != nil {
			return err
		}
		if _, ok := groups[k]; !ok {
			keysInOrder = append(keysInOrder, k)
		}
		groups[k] = append(groups[k], i)
	}
	out := make([]Row, 0, len(groups))
	keysSorted := append([]string(nil), keysInOrder...)
	sort.Strings(keysSorted)
	for _, k := range keysInOrder {
		rep := env.Rows[groups[k][0]]
		if rep.Vars == nil {
			rep.Vars = map[string]string{}
		}
		rep.Vars["group.keys"] = strings.Join(keysSorted, ",")
		out = append(out, rep)
	}
	env.Rows = out
	return nil
}
