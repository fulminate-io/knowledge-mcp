// SPDX-License-Identifier: Apache-2.0

package recipe

import (
	"context"
	"sort"
	"strings"
)

// evalSelect replaces env.Rows with the set of nodes of the given
// type matching the optional where clause. Reads from the in-memory
// sourceView's by-type index (the materialized source graph already
// excluded tombstones at load time). Empty selections are valid —
// subsequent rules short-circuit naturally on an empty row set.
func evalSelect(ctx context.Context, env *Env, r RuleSelect, sv *sourceView) error {
	nodes := sv.nodesByType(r.NodeType)
	rows := make([]Row, 0, len(nodes))
	for _, n := range nodes {
		rows = append(rows, Row{NodeID: n.Id, Node: n, Vars: map[string]string{}})
	}
	if r.Where != nil {
		kept := rows[:0]
		for i := range rows {
			v, err := evalExpr(ctx, env, &rows[i], r.Where, sv)
			if err != nil {
				return err
			}
			if v != "" {
				kept = append(kept, rows[i])
			}
		}
		rows = kept
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
func evalTraverse(_ context.Context, env *Env, r RuleTraverse, sv *sourceView) error {
	dir, err := parseDirection(r.Direction)
	if err != nil {
		return err
	}
	out := make([]Row, 0, len(env.Rows))
	for i := range env.Rows {
		prev := &env.Rows[i]
		for _, targetID := range sv.edgesFrom(prev.NodeID, r.EdgeType, dir) {
			// Resolve the target node for field access.
			target, ok := sv.nodeByID(targetID)
			if !ok {
				// Skip orphan edges whose target isn't in the graph —
				// same defensive behavior as existing traversal utilities.
				continue
			}
			vars := cloneRowVars(prev.Vars)
			if r.As != "" {
				vars[r.As] = targetID
			}
			out = append(out, Row{NodeID: targetID, Node: target, Vars: vars})
		}
	}
	env.Rows = out
	return nil
}

// evalFilter drops rows where the predicate evaluates to the empty
// string. Non-empty strings (including "true", match text, bound var
// values) count as truthy.
func evalFilter(ctx context.Context, env *Env, r RuleFilter, sv *sourceView) error {
	kept := env.Rows[:0]
	for i := range env.Rows {
		v, err := evalExpr(ctx, env, &env.Rows[i], r.Pred, sv)
		if err != nil {
			return err
		}
		if v != "" {
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
