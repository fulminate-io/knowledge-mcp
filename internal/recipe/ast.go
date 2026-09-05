// SPDX-License-Identifier: Apache-2.0

// Package recipe implements the graph-agnostic recipe DSL: a pure-text
// rule language that declares how to project a source graph into a
// target graph. It runs entirely CLIENT-SIDE — the client reads the
// source graph over the wire, interprets the recipe in memory, and
// ships the projected nodes/edges back through the collector Sink. The
// package exposes:
//
//   - ast.go — AST types (this file).
//   - lexer.go — byte-scanning lexer with line:col tracking.
//   - parser.go — recursive-descent parser producing *Recipe.
//   - source_view.go — the in-memory source-graph reader, hydrated once
//     via two Execute RPCs (FetchAllNodes + FetchEdges).
//   - interpret*.go — evaluator that walks the AST against the in-memory
//     sourceView and accumulates emissions into an in-memory Result.
//   - run_recipe.go — the single RunRecipe entry point the `collect`
//     tool dispatches to: load recipe → interpret → write via the Sink.
//
// Recipe bodies are stored in GraphTransformers as node Content. The
// interpreter is zero-LLM — every decision is driven by field access,
// regex matches, string concat/trim/lower/upper, has_edge, and var
// bindings. No arithmetic, no control flow beyond the rule sequence.
package recipe

// Position carries the line:col of an AST node's source token. All
// errors reported by the lexer, parser, and interpreter cite a Position
// so users can fix the recipe at the exact offending location. Line
// and Col are both 1-indexed (the first character of the first line
// is Line=1 Col=1).
type Position struct {
	Line int
	Col  int
}

// Recipe is the top-level parse result: an ordered list of rules. The
// rule order is the execution order — each rule's output flows into
// the next, mirroring SQL's FROM/WHERE/SELECT pipeline. Pos points at
// the first rule's starting token (or 1:1 for an empty recipe).
type Recipe struct {
	Rules []Rule
	Pos   Position
}

// Rule is the sum type implemented by every concrete rule variant. The
// two marker methods — isRule() as a type discriminator and Position()
// for error reporting — keep the interpreter's type switch cheap and
// error messages uniform across variants.
type Rule interface {
	isRule()
	Position() Position
}

// RuleSelect opens a rule sequence with a node-type selection. Where
// is the optional where-tree applied per candidate node; a nil Where
// means "every node of NodeType", which is what an omitted `where`
// clause parses to.
type RuleSelect struct {
	NodeType string
	Where    *WhereNode
	Pos      Position
}

func (r RuleSelect) isRule()            {}
func (r RuleSelect) Position() Position { return r.Pos }

// RuleTraverse walks outgoing/incoming/bidirectional edges of the
// given EdgeType from each row, binding the destination node under the
// name As (without the leading "$"). Direction is one of "in", "out",
// "both" — the parser normalizes case.
type RuleTraverse struct {
	EdgeType  string
	Direction string
	As        string
	Pos       Position
}

func (r RuleTraverse) isRule()            {}
func (r RuleTraverse) Position() Position { return r.Pos }

// RuleWalk descends a node's SUBTREE along EdgeType and replaces the rowset with
// every reachable descendant, in document reading order, binding each under the
// name As (without the leading "$") when one is given.
//
// It is not a repeated traverse. A traverse expands one level and replaces the
// rowset with that level, so an outline needs one rule per level and returns
// them in blocks; a walk returns the whole subtree interleaved as the document
// reads, with walk.depth on every row saying which level it came from.
//
// THERE IS NO DEPTH CLAUSE. Narrowing by level is a filter on walk.depth, and
// the rowset is bounded by the in-memory source graph exactly as select's is.
type RuleWalk struct {
	EdgeType string
	As       string
	Pos      Position
}

func (r RuleWalk) isRule()            {}
func (r RuleWalk) Position() Position { return r.Pos }

// RuleFilter drops every row whose Where tree evaluates false.
//
// A NIL Where IS IMPOSSIBLE, because the parser requires the tree: `filter`
// with no brace-delimited where-tree is a parse error. That is worth stating
// here rather than defending against, because the defensive branch a later
// reader would add — `if r.Where == nil { keep the row }` — is dead code that
// silently converts a parser bug into a filter matching everything, which is
// exactly the class this grammar replaced.
type RuleFilter struct {
	Where *WhereNode
	Pos   Position
}

func (r RuleFilter) isRule()            {}
func (r RuleFilter) Position() Position { return r.Pos }

// RuleBind stores the evaluation of Value under Var (without the
// leading "$") on every current row. Subsequent rules may read the
// value via $Var in their expressions.
type RuleBind struct {
	Var   string
	Value Expr
	Pos   Position
}

func (r RuleBind) isRule()            {}
func (r RuleBind) Position() Position { return r.Pos }

// RuleGroupBy collapses rows whose Key expression evaluates to the
// same value into one row. The resulting row carries a synthetic
// `group.keys` pseudo-variable exposing the comma-joined per-row
// originals the interpreter chose to keep (ordering matches selection
// order before the collapse).
type RuleGroupBy struct {
	Key Expr
	Pos Position
}

func (r RuleGroupBy) isRule()            {}
func (r RuleGroupBy) Position() Position { return r.Pos }

// RuleEmit writes a target-graph node per row. Fields is the ordered
// key-expression map used to set node fields — the top-level keys
// `type`, `name`, `summary`, `content`, `description`, `source` map to
// the Node struct directly; any other key lands in Metadata. The As
// binding records (sourceRowID → emittedNodeID) in the interpreter's
// cross-emit map so later RuleLink rules can reference target IDs
// emitted in earlier rules.
type RuleEmit struct {
	NodeType string
	Fields   map[string]Expr
	As       string
	Pos      Position
}

func (r RuleEmit) isRule()            {}
func (r RuleEmit) Position() Position { return r.Pos }

// RuleLookup computes the StableID that a previously-emitted target
// node would have (given NodeType + Identity + the current run's
// sourceSlug) and binds it to As on the cross-emit map — without
// writing a node. Used by recipes that want to link to targets already
// emitted by earlier rules without paying the emit-upsert + lineage
// cost per row.
//
// Semantics:
//   - Identity evaluates to a string; combined with target graph /
//     sourceSlug / NodeType via StableID exactly as RuleEmit does.
//   - The interpreter verifies the node was emitted earlier in THIS run
//     (an in-run emitted-set check — same-run scope, no cross-run target
//     read). If it was, As is bound on the cross-emit map for the
//     current row. If the node was not emitted (or identity evaluates to
//     empty), no binding is made and Stats.LookupMisses increments —
//     subsequent RuleLink operations targeting As silently skip for that
//     row.
type RuleLookup struct {
	NodeType string
	Identity Expr
	As       string
	Pos      Position
}

func (r RuleLookup) isRule()            {}
func (r RuleLookup) Position() Position { return r.Pos }

// RuleLink creates an edge between two target nodes identified by
// From and To expressions. Both sides typically reference cross-emit
// bindings made by prior RuleEmit `as $var` clauses.
type RuleLink struct {
	From Expr
	Rel  string
	To   Expr
	Pos  Position
}

func (r RuleLink) isRule()            {}
func (r RuleLink) Position() Position { return r.Pos }

// RuleSourceRef declares that Ref's resolved value is the source node
// ID every subsequent emit in this rule sequence points back to via a
// translated-from edge. Without a RuleSourceRef, the interpreter
// defaults to the current row's node ID.
type RuleSourceRef struct {
	Ref Expr
	Pos Position
}

func (r RuleSourceRef) isRule()            {}
func (r RuleSourceRef) Position() Position { return r.Pos }

// Expr is the sum type implemented by every expression variant. Like
// Rule, it carries a marker and a Position accessor.
type Expr interface {
	isExpr()
	Position() Position
}

// ExprField is a dotted-path field reference like section.name or
// $var.metadata.key. Path is pre-split on ".", with a leading "$" on
// the first segment preserved so the evaluator can detect var-prefixed
// references without re-parsing.
type ExprField struct {
	Path []string
	Pos  Position
}

func (e ExprField) isExpr()            {}
func (e ExprField) Position() Position { return e.Pos }

// ExprLit is a string literal. v1 only supports string literals —
// numeric, boolean, and null literals are out of scope per the ticket.
type ExprLit struct {
	Value string
	Pos   Position
}

func (e ExprLit) isExpr()            {}
func (e ExprLit) Position() Position { return e.Pos }

// ExprVar is a bare $var reference. The evaluator looks up the value
// on the current row first (per-row bindings), falling back to the
// recipe-wide Env.Vars.
type ExprVar struct {
	Name string
	Pos  Position
}

func (e ExprVar) isExpr()            {}
func (e ExprVar) Position() Position { return e.Pos }

// ExprRegex is the `LHS ~= /pattern/` (or `LHS !~ /pattern/`) operator.
// The evaluator computes LHS as a string and tests Pattern against it;
// the result is "" (no match) or the full match text (matches) for `~=`.
// When Negate=true (the `!~` spelling), the truthy/falsy sense is
// inverted: an unmatched LHS yields a non-empty sentinel so filter
// predicates trigger, and a match yields "".
type ExprRegex struct {
	LHS     Expr
	Pattern string
	Negate  bool
	Pos     Position
}

func (e ExprRegex) isExpr()            {}
func (e ExprRegex) Position() Position { return e.Pos }

// ExprFunc is a call to one of the v1 builtins: concat, trim, lower,
// upper, has_edge. Name carries the function name; Args is the list of
// argument expressions. The evaluator enforces arity and type per
// builtin.
type ExprFunc struct {
	Name string
	Args []Expr
	Pos  Position
}

func (e ExprFunc) isExpr()            {}
func (e ExprFunc) Position() Position { return e.Pos }
