// SPDX-License-Identifier: Apache-2.0

package recipe

import (
	"context"
	"fmt"
	"slices"
	"sync/atomic"
)

// where_eval.go — evaluating a where-tree against one sourceView row.
//
// EVERYTHING IT READS ALREADY EXISTS IN THIS PACKAGE. evalField is the `of`
// reader and already handles node fields, the metadata fall-through, the
// virtual `body` coalesce, `$var` references and the row-scoped group.keys
// pseudo-variable; sv.edgesAlong is the descendant walk (edgesFrom with the edge
// kept, so the candidate row knows which edge reached it) and hasAncestor's
// bounded visited-set walk is the ancestor walk's shape. Nothing here imports
// tree-sitter, and nothing here can delegate to ast's evaluator: that one is
// typed on *sitter.Node, a CGO struct no other package can construct.
//
// Cost per row: one map read per field leaf, and for the two edge leaves a
// bounded walk over edge slices that are already resident. No wire round trip on
// any path, no regex compiled here, and no census computed here.

// regexCompiles counts the VALIDATOR COMPILE PASS'S CALLS into compileRegex —
// one per literal regex per run, CACHE HITS INCLUDED. compileRegex is backed by
// the process-global regexCache, so a call that finds its pattern already
// compiled still increments this; the counter measures calls, not compilations.
//
// That is enough for the property it exists to make measurable: "every literal
// regex is compiled once per run" rather than once per row. A lazy per-row
// compile would move this counter by the ROW COUNT, and no correctness test can
// see such a fallback, because it returns identical answers on every input.
// Nothing the evaluator does increments it, because the evaluator compiles
// nothing.
//
// replication: process-local by design — this is a client-side measurement aid
// with no replicated meaning. It counts calls within one process, a restart
// re-zeroes it, and no reader outside this process consults it.
var regexCompiles atomic.Int64

// maxWhereWalkDepth caps both edge-leaf walks at 32 hops, matching hasAncestor
// and maxRenderDepth.
//
// IT IS A TRUNCATION, NOT A TERMINATION GUARD. The visited set is what makes
// these walks terminate: every node enters the frontier at most once, so a
// cyclic source graph cannot hang a run with or without this bound. What the
// bound does is stop one pathological document from letting a single leaf
// dominate a run — and the cost of that is real: on a 50-node chain, 32 of the
// 49 sections that genuinely have the document as an ancestor match, and the
// other 17 report FALSE with no error. A match further than 32 hops from the row
// is not found.
const maxWhereWalkDepth = 32

// evalWhereTree reports whether a row satisfies a where-tree.
//
// COMPOSERS MIRROR ast's evalWhere, including the implicit AND when several are
// set on one node: all short-circuits on the first false, any on the first true,
// not inverts, and a nil node is true.
func evalWhereTree(ctx context.Context, env *Env, row *Row, w *WhereNode, sv *sourceView) (bool, error) {
	if w == nil {
		return true, nil
	}

	for _, child := range w.All {
		ok, err := evalWhereTree(ctx, env, row, child, sv)
		if err != nil || !ok {
			return false, err
		}
	}
	if len(w.Any) > 0 {
		matched := false
		for _, child := range w.Any {
			ok, err := evalWhereTree(ctx, env, row, child, sv)
			if err != nil {
				return false, err
			}
			if ok {
				matched = true
				break
			}
		}
		if !matched {
			return false, nil
		}
	}
	if w.Not != nil {
		ok, err := evalWhereTree(ctx, env, row, w.Not, sv)
		if err != nil {
			return false, err
		}
		if ok {
			return false, nil
		}
	}

	return evalWhereLeaves(ctx, env, row, w, sv)
}

// evalWhereLeaves evaluates the leaves carried by one node, AND-ed together.
func evalWhereLeaves(ctx context.Context, env *Env, row *Row, w *WhereNode, sv *sourceView) (bool, error) {
	if w.Kind != nil {
		ok, err := evalKindLeaf(env, row, w.Kind)
		if err != nil || !ok {
			return false, err
		}
	}
	if w.Matches != nil {
		ok, err := evalMatchesLeaf(env, row, w.Matches)
		if err != nil || !ok {
			return false, err
		}
	}
	if w.Equals != nil {
		v, err := readOf(env, row, w.Equals.Of)
		if err != nil {
			return false, err
		}
		if v != w.Equals.Value {
			return false, nil
		}
	}
	// EXISTS IS TRUE WHEN THE FIELD PATH RESOLVES NON-EMPTY, and there is no
	// third state to it. This DSL has no null: every field read yields a string,
	// and an absent key and an empty value are the same value. So `exists` and an
	// `equals` against "" are EXACT COMPLEMENTS. That looks like a gap to a
	// reader who expects a null-aware language, and the repair such a reader
	// reaches for — a distinct "present but empty" state — has nothing in the
	// data to distinguish, because the graph never stored one.
	if w.Exists != nil {
		v, err := readOf(env, row, w.Exists.Of)
		if err != nil {
			return false, err
		}
		if v == "" {
			return false, nil
		}
	}
	if w.Ancestor != nil {
		ok, err := evalEdgeLeaf(ctx, env, row, w.Ancestor, sv, incomingEdges)
		if err != nil || !ok {
			return false, err
		}
	}
	if w.Descendant != nil {
		ok, err := evalEdgeLeaf(ctx, env, row, w.Descendant, sv, outgoingEdges)
		if err != nil || !ok {
			return false, err
		}
	}
	if w.Compare != nil {
		ok, err := evalCompareLeaf(env, row, w.Compare)
		if err != nil || !ok {
			return false, err
		}
	}
	return true, nil
}

// evalCompareLeaf applies the operator and operand the VALIDATOR resolved.
//
// IT READS THE RUN'S MAP AND WRITES NOTHING. There is no lazy resolve and no
// memo onto the leaf, on exactly the terms evalMatchesLeaf states: the cached
// AST is read-only, a leaf missing from the map is the validator bug it is
// rather than a silently re-resolved one, and the absence of the fallback is
// what makes the rule enforceable instead of aspirational.
//
// BAD INPUT AND A FALSE PREDICATE ARE DIFFERENT THINGS, AND THIS FUNCTION KEEPS
// THEM APART. There are two distinct absences and they get opposite treatments:
//
//   - A key the SOURCE GRAPH'S VOCABULARY does not carry — no node in the loaded
//     graph ever stamped it — is BAD INPUT. It is REFUSED before the walk by the
//     metadata-key census, which reaches this leaf's `of` through
//     whereNodeOwnPaths. It never gets here.
//   - A CENSUSED key merely MISSING ON THIS ROW is a FALSE PREDICATE. The recipe
//     named something real; this row does not have it; the predicate is false for
//     this row and the run continues. NOT-MATCHING IS NOT COERCION: nothing is
//     defaulted to zero, nothing is compared, and the row is not reclassified. An
//     implementation that coerced absent to 0 would be the coercion the rule
//     forbids, and it would land every unstamped row in the `lt` set.
//
// The corpus reads it that way already. evalWhereLeaves' comment on the exists
// leaf records that this DSL has no null and that an absent key and an empty
// value are the same value. lineage.go's positionFromEvidence rules an absent or
// unparseable contains-edge position a property of the SOURCE GRAPH rather than
// an author mistake. And sourceView.buildCensus makes the metadata census a
// graph-WIDE union precisely so a key only one node type stamps stays legal — so
// a correct recipe routinely compares a key most rows lack, and erroring on
// absent would abort it on its first unstamped node. `exists` beside the compare
// is the discriminator for an author who wants the other reading.
//
// A NON-EMPTY value that does not parse is NEITHER absence: something wrote text
// where the recipe expects a magnitude, and that errors naming the node and the
// value.
func evalCompareLeaf(env *Env, row *Row, leaf *CompareLeaf) (bool, error) {
	resolved, ok := env.whereCompares[leaf]
	if !ok {
		return false, fmt.Errorf(
			"recipe: compare leaf on %q (op %q, value %q) reached the evaluator unresolved, which is a validator bug: "+
				"validateAgainstSource must resolve every compare leaf before the row loop",
			leaf.Of, leaf.Op, leaf.Value)
	}
	v, err := readOf(env, row, leaf.Of)
	if err != nil {
		return false, err
	}
	if v == "" {
		return false, nil
	}
	got, err := parseNumericOperand(v)
	if err != nil {
		return false, fmt.Errorf(
			"recipe: compare leaf on %q read %q off node %q, which is not a number: %w",
			leaf.Of, v, row.NodeID, err)
	}
	return compareOrdered(resolved.op, got, resolved.value)
}

// readOf reads a where-tree `of` field path off the row.
//
// AN UNRESOLVABLE REFERENCE IS AN ERROR, NOT FALSE. Treating it as false is the
// wrong-but-compiling implementation that silently drops every row and
// reintroduces the exact class this grammar replaced; ast made the same call for
// the same reason.
func readOf(env *Env, row *Row, of string) (string, error) {
	path := splitFieldPath(of)
	if of == "" || len(path) == 0 {
		return "", fmt.Errorf("recipe: where-tree leaf has an empty `of` — name the field path it should read")
	}
	if row == nil {
		return "", fmt.Errorf("recipe: where-tree leaf reads %q with no row to read it from", of)
	}
	return evalField(env, row, path)
}

// evalKindLeaf compares the TYPE of the row named by Of against the leaf's
// accepted types.
//
// It reads through evalField with "type" appended rather than reaching for
// row.Node.Type directly, so `of: "node"` and `of: "<traverse alias>"` resolve
// on the one path every other leaf uses.
func evalKindLeaf(env *Env, row *Row, leaf *KindLeaf) (bool, error) {
	if leaf.Of == "" {
		return false, fmt.Errorf("recipe: kind leaf has an empty `of` — name the row whose type it should test")
	}
	if row == nil {
		return false, fmt.Errorf("recipe: kind leaf reads %q with no row to read it from", leaf.Of)
	}
	got, err := evalField(env, row, append(splitFieldPath(leaf.Of), "type"))
	if err != nil {
		return false, err
	}
	if slices.Contains(leaf.Is, got) {
		return true, nil
	}
	return false, nil
}

// evalMatchesLeaf applies the regex the VALIDATOR compiled.
//
// THERE IS NO regexp.Compile CALL AND NO LAZY-COMPILE FALLBACK IN THIS FILE, and
// the absence is the enforcement. A fallback of the shape
// `if l.compiled == nil { compile }` returns identical answers on every input,
// so no correctness test can see it; only its cost differs, measured at 23.65
// ns/op and 0 allocations compiled-once against 842.5 ns/op and 30 allocations
// per row. A leaf whose regex is missing from the run's compiled map is
// therefore reported as the validator bug it is, which is what makes the absence
// enforceable.
func evalMatchesLeaf(env *Env, row *Row, leaf *MatchesLeaf) (bool, error) {
	compiled := env.whereRegexes[leaf]
	if compiled == nil {
		return false, fmt.Errorf(
			"recipe: matches leaf on %q reached the evaluator with an uncompiled regex %q, which is a validator bug: "+
				"validateAgainstSource must compile every literal regex before the row loop",
			leaf.Of, leaf.Regex)
	}
	v, err := readOf(env, row, leaf.Of)
	if err != nil {
		return false, err
	}
	return compiled.MatchString(v), nil
}

// evalEdgeLeaf walks transitively from the row along the leaf's edge type and
// reports whether any reached node satisfies the sub-tree.
//
// dir is incomingEdges for an ancestor leaf and outgoingEdges for a descendant
// one; the walk is otherwise identical, which is why one EdgeLeaf type serves
// both.
//
// EACH WALKED NEIGHBOR IS TESTED AS A SYNTHETIC ROW CARRYING THE OUTER ROW'S
// Vars — carried, not cloned fresh — so a `$var` bound before the walk resolves
// inside the sub-tree against the row that bound it. A neighbor whose node is
// absent from the view is skipped, matching evalTraverse's orphan-edge
// behaviour on a malformed source graph.
//
// ITS Edge IS ITS OWN, NOT THE OUTER ROW'S, and that is the opposite choice from
// Vars above. The candidate is a DIFFERENT row, reached along a DIFFERENT edge,
// so inside a walk sub-tree `edge` names the WALKED edge and shadows any outer
// traverse's. Carrying the outer row's edge down instead reads plausibly and
// answers the wrong question — a compare over `edge.position` inside an ancestor
// leaf would test the position the OUTER row was reached at, on every neighbor.
func evalEdgeLeaf(
	ctx context.Context,
	env *Env,
	row *Row,
	leaf *EdgeLeaf,
	sv *sourceView,
	dir edgeDirection,
) (bool, error) {
	if row == nil || row.NodeID == "" {
		return false, nil
	}
	visited := map[string]bool{row.NodeID: true}
	frontier := []string{row.NodeID}
	for depth := 0; depth < maxWhereWalkDepth && len(frontier) > 0; depth++ {
		next := frontier[:0:0]
		for _, nodeID := range frontier {
			for _, hop := range sv.edgesAlong(nodeID, leaf.Edge, dir) {
				neighborID := hop.NodeID
				if visited[neighborID] {
					continue
				}
				visited[neighborID] = true
				node, ok := sv.nodeByID(neighborID)
				if !ok {
					continue
				}
				candidate := Row{NodeID: neighborID, Node: node, Vars: row.Vars, Edge: hop.Edge}
				matched, err := evalWhereTree(ctx, env, &candidate, leaf.Where, sv)
				if err != nil {
					return false, err
				}
				if matched {
					return true, nil
				}
				next = append(next, neighborID)
			}
		}
		frontier = next
	}
	return false, nil
}
