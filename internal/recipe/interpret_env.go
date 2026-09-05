// SPDX-License-Identifier: Apache-2.0

package recipe

import (
	"maps"
	"regexp"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// Env carries the binding environment across rules in a single recipe
// run. Named variables map to either a single node ID (from traverse /
// emit as) or a single scalar value (from bind). The cross-emit map
// tracks (source_node_id → target_node_id) for each `as $var` on an
// emit, enabling later `link` rules to reference targets emitted in
// earlier rules.
//
// Env is mutable across the interpreter's rule loop — the rule
// evaluators replace Rows, merge into Vars, and append to EmitMap in
// place. Interpret() owns the Env lifecycle; callers never touch it.
type Env struct {
	// Rows are the current selection — one entry per source row
	// flowing through the rule pipeline after select/traverse/filter.
	Rows []Row

	// Vars maps $var → value or node ID bound by `bind` / `traverse as`.
	// When a var is bound at traverse time, each Row carries its own
	// value for that var (see Row.Vars). Global bindings (e.g. from
	// a `bind $x := "hello"` rule against a one-row selection) end up
	// on every row's Vars AND the outer Env.Vars so later rules can
	// consult either scope.
	Vars map[string]string

	// EmitMap is the cross-emit binding store. emit with `as $var`
	// stamps (sourceRowID → targetNodeID) here; later `link` rules
	// look up target IDs by source row. Outer map keyed by var name
	// (without the leading "$"); inner map keyed by the source row
	// NodeID at emit time.
	EmitMap map[string]map[string]string

	// SourceRef is the node ID the next emit should stamp its
	// translated-from edge against. When empty, the interpreter falls
	// back to the current row's NodeID — the typical case. A prior
	// RuleSourceRef populates this explicitly so emitted nodes can
	// point at a different source anchor (e.g. a page rather than the
	// current section).
	SourceRef string

	// whereRegexes holds the regexes the validator compiled for this run's
	// where-tree matches leaves, keyed by the leaf each belongs to.
	//
	// IT LIVES HERE, PER RUN, RATHER THAN ON THE LEAF, because the leaf belongs
	// to a *Recipe shared through the process-global astCache: storing the
	// compiled expression on it was an unsynchronized write into state two
	// concurrent runs both touch, and a reader that saw it as nil refused a
	// valid recipe. An Env is built fresh per Interpret call, so this map is
	// private to one run by construction.
	whereRegexes map[*MatchesLeaf]*regexp.Regexp

	// whereCompares holds the operator and operand the validator resolved for
	// this run's where-tree compare leaves, keyed by the leaf each belongs to.
	//
	// IT LIVES HERE, PER RUN, RATHER THAN ON THE LEAF, on exactly the terms
	// whereRegexes does and for the same measured reason: the leaf belongs to a
	// *Recipe shared through the process-global astCache, so resolved state
	// stored on it would be an unsynchronized write into state two concurrent
	// runs both touch. An Env is built fresh per Interpret call, so this map is
	// private to one run by construction.
	whereCompares map[*CompareLeaf]compareResolution
}

// compareResolution is one compare leaf's resolved operator and parsed operand,
// both settled by the validator BEFORE any row exists. The evaluator reads it
// and writes nothing.
type compareResolution struct {
	op    knowledgev1.MetadataPredicate_Op
	value float64
}

// Row is a single row of the interpreter's working selection. NodeID
// identifies the source-graph node; Node is the wire node from the
// in-memory sourceView (cheap — the whole source graph is already
// materialized in memory by loadSourceView before interpretation
// begins). Per-row Vars hold traverse-as bindings that naturally differ
// across rows.
type Row struct {
	NodeID string
	Node   *knowledgev1.Node
	Vars   map[string]string

	// Edge is the SOURCE-GRAPH EDGE THAT PRODUCED THIS ROW — the edge a
	// traverse rule or a where-tree walk leaf stepped along to reach it. It is
	// nil when the row came from a select, which walked no edge to find it.
	//
	// It exists because sourceView.edgesFrom projects every matching edge down
	// to a neighbor ID, which puts the edge's own Type and Evidence — where
	// both raw collectors stamp the contains position — structurally out of
	// reach of any field reader. Carrying the handle on the row is what makes
	// `edge.…` answerable at all.
	//
	// PER-ROW STATE, so the cached-AST rule is satisfied by construction rather
	// than by care: rows are built fresh by the walker on every run, and
	// nothing here is written onto a WhereNode, a leaf, or anything else
	// hanging off the memoized *Recipe.
	Edge *knowledgev1.Edge
}

// newEnv returns a freshly initialized Env. Used by Interpret() at the
// top of a run and by tests that want to exercise rule evaluators
// directly.
func newEnv() *Env {
	return &Env{
		Vars:          map[string]string{},
		EmitMap:       map[string]map[string]string{},
		whereRegexes:  map[*MatchesLeaf]*regexp.Regexp{},
		whereCompares: map[*CompareLeaf]compareResolution{},
	}
}

// cloneRowVars returns a shallow copy of vars so a rule evaluator can
// mutate the per-row binding set without aliasing the parent row.
// Nil input produces a fresh empty map, matching Row.Vars' zero state.
func cloneRowVars(vars map[string]string) map[string]string {
	if vars == nil {
		return map[string]string{}
	}
	out := make(map[string]string, len(vars)+1)
	maps.Copy(out, vars)
	return out
}

// rememberEmit stamps (sourceRowID → targetNodeID) under varName on
// the cross-emit map AND on the per-row Vars so the binding survives
// forward through subsequent `traverse` rules (cloneRowVars preserves
// it into each traverse-target row). Writing to both stores keeps
// EmitMap as a per-run audit trail while making Vars the canonical
// lookup path for `link` rule endpoint resolution. No-op when varName
// is empty (the `emit` had no `as $var` clause).
func (env *Env) rememberEmit(varName, sourceRowID, targetNodeID string, row *Row) {
	if varName == "" {
		return
	}
	bucket, ok := env.EmitMap[varName]
	if !ok {
		bucket = map[string]string{}
		env.EmitMap[varName] = bucket
	}
	bucket[sourceRowID] = targetNodeID
	if row != nil {
		if row.Vars == nil {
			row.Vars = map[string]string{}
		}
		row.Vars[varName] = targetNodeID
	}
}
