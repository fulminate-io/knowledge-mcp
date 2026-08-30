// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recall_mode_census_test.go — the BOTH-DIRECTIONS census between the recall
// mode vocabulary the CODE routes and the enum the thoughts schema publishes.
//
// WHY IT EXISTS. The schema shipped Enum{search,timeline,charges,graph,clusters}
// while handleRecallClient routed a sixth, mode:"context" — the session-start
// context pack the /research skill instructs agents to call BY NAME. The enum is
// not enforced (recall validates params by name, not by value), so the mode
// worked; it was simply invisible to any caller reading tools/list, which is the
// only place an LLM can learn it exists. A mode that runs and cannot be
// discovered is the worst of both: shipped, documented in a skill, and absent
// from the contract.
//
// ANCHORED ON THE DISPATCH, NOT ON A COPY. Every routed mode below is read out
// of the SOURCE of the three sites that consume `mode` — handleRecallClient's
// arm comparisons, FormatRecallResults' render switch, and renderRecallResults'
// empty-mode default. There is no live Go slice to point at (each site tests the
// string inline), so the anchor is the syntax of those sites. A hand-copied list
// here would go green on the day it drifted and this file would then be
// documenting the drift rather than catching it — which is exactly how the enum
// got into the state that motivated the census.

// The three consumption sites, named so a relocation fails loudly at the lookup
// rather than silently shrinking the routed set to nothing.
const (
	recallDispatchFunc = "handleRecallClient"
	recallDefaultFunc  = "renderRecallResults"
	recallRenderFile   = "../thought/query.go"
	recallRenderFunc   = "FormatRecallResults"
)

// recallDefaultRenderAliases are enum members with NO dispatch arm of their own:
// FormatRecallResults' default branch renders them exactly as "search" does.
//
// This one list is declared rather than derived, because an alias is precisely a
// value the dispatch never mentions — there is nothing in the source to derive
// it from. That makes it the census's only hand-written input, and it is
// deliberately load-bearing in the strict direction: a NEW enum member that is
// not wired and not written down here lands red, so the choice between "wire it"
// and "declare it a synonym" has to be made explicitly rather than by default.
var recallDefaultRenderAliases = []string{"graph"}

// routedRecallModes returns the recall mode vocabulary the code actually
// consumes, deduped and sorted. Three sources, all read from source syntax:
//
//	handleRecallClient   — `a.Mode == "<lit>"` arm comparisons (clusters, context)
//	FormatRecallResults  — `switch mode { case "<lit>": }` renders (timeline, charges)
//	renderRecallResults  — the `mode = "<lit>"` empty-mode default (search)
func routedRecallModes(t *testing.T) []string {
	t.Helper()
	pkg := parseToolsPackage(t)

	dispatch, ok := pkg.funcs[recallDispatchFunc]
	require.Truef(t, ok, "%s not found in this package — the census has lost its dispatch anchor", recallDispatchFunc)
	defaults, ok := pkg.funcs[recallDefaultFunc]
	require.Truef(t, ok, "%s not found in this package — the census has lost its default anchor", recallDefaultFunc)
	render := parseFuncFromFile(t, recallRenderFile, recallRenderFunc)

	var modes []string
	modes = append(modes, modeLiteralsCompared(dispatch)...)
	modes = append(modes, switchCaseLiteralsOn(render, "mode")...)
	modes = append(modes, stringLitsAssignedTo(defaults, "mode")...)
	return sortedSet(modes)
}

// TestRecallModeEnum_PublishesEveryRoutedMode is the direction the enum failed:
// a mode the code routes but the schema does not publish is a capability no
// caller can discover.
func TestRecallModeEnum_PublishesEveryRoutedMode(t *testing.T) {
	routed := routedRecallModes(t)
	require.NotEmpty(t, routed, "no routed modes extracted — the walker is broken and the census would be vacuous")

	// WALKER-LIVENESS CONTROL. Each of these three is contributed by a DIFFERENT
	// one of the three extractions, so a walker that lost any single source is
	// caught here rather than reported as a clean census. "context" is named
	// because it is the arm the enum was missing.
	for _, arm := range []string{"context", "charges", "search"} {
		require.Containsf(t, routed, arm,
			"the walker did not find the routed mode %q — one of the three extraction sources is no longer matching", arm)
	}

	enum := ThoughtsToolDef().InputSchema.Properties["mode"].Enum
	require.NotEmpty(t, enum, "the thoughts schema publishes no recall mode enum — the census would be vacuous")

	assert.Empty(t, valuesMissingFrom(enum, routed),
		"the thoughts recall mode enum omits a mode the dispatch routes; a caller reading tools/list cannot discover it")

	// KNOWN-POSITIVE CONTROL, same instrument, same run. Hole the enum and the
	// census must NAME the removed mode. Without this a matcher broken into
	// always-satisfied would report the identical empty slice as a complete enum.
	holed := withoutValue(enum, "clusters")
	require.NotEqual(t, len(enum), len(holed), "control setup failed: 'clusters' was not in the enum to remove")
	assert.Equal(t, []string{"clusters"}, valuesMissingFrom(holed, routed),
		"the census must report exactly the mode removed from the enum — if it reports none, it cannot fail")
}

// TestRecallModeEnum_DeclaresNoModeTheDispatchIgnores is the inverse direction.
// An enum member the dispatch never mentions either renders through the default
// branch — in which case it is a synonym and says so in
// recallDefaultRenderAliases — or it is a value the schema invites callers to
// send and nothing honors.
func TestRecallModeEnum_DeclaresNoModeTheDispatchIgnores(t *testing.T) {
	routed := routedRecallModes(t)
	enum := ThoughtsToolDef().InputSchema.Properties["mode"].Enum
	require.NotEmpty(t, enum, "the thoughts schema publishes no recall mode enum — the census would be vacuous")

	honored := sortedSet(append(append([]string{}, routed...), recallDefaultRenderAliases...))
	assert.Empty(t, valuesMissingFrom(honored, enum),
		"the recall mode enum publishes a value no dispatch arm routes; wire it, or record it in "+
			"recallDefaultRenderAliases as a declared search-render synonym")

	// KNOWN-POSITIVE CONTROL: a phantom enum member must be named.
	assert.Equal(t, []string{"phantom"}, valuesMissingFrom(honored, append(append([]string{}, enum...), "phantom")),
		"the census must report an enum value nothing honors — if it reports none, it cannot fail")

	// The alias list is itself checked, in both directions, so it cannot become a
	// dumping ground that silently absorbs a mode someone forgot to wire.
	for _, alias := range recallDefaultRenderAliases {
		assert.Containsf(t, enum, alias,
			"recallDefaultRenderAliases records %q, which the enum does not publish — a synonym for a value no caller can send", alias)
		assert.NotContainsf(t, routed, alias,
			"recallDefaultRenderAliases records %q as unrouted, but the dispatch now routes it — it is an arm, not a synonym", alias)
	}
}

// TestThoughtsToolDescription_ModesLineMatchesTheEnum pins the prose half. The
// description is shipped in the same tools/list payload as the enum and is the
// list a reader actually copies, so the two disagreeing is the identical defect
// one level over: the enum had five and so did the sentence.
func TestThoughtsToolDescription_ModesLineMatchesTheEnum(t *testing.T) {
	enum := ThoughtsToolDef().InputSchema.Properties["mode"].Enum
	require.NotEmpty(t, enum, "the thoughts schema publishes no recall mode enum — the census would be vacuous")

	listed := recallModesNamedInDescription(t, thoughtsToolDescription)
	require.NotEmpty(t, listed, "no Modes list parsed out of thoughtsToolDescription — the census would be vacuous")

	assert.Equal(t, sortedSet(enum), listed,
		"the thoughts tool description's recall Modes list and the published mode enum disagree")
}

// recallModesNamedInDescription extracts the recall arm's "Modes: ..." list from
// the tool description: the span between "Modes:" and the sentence-ending period
// that closes it, split on commas with the "(default)" annotation stripped.
//
// Parsing the LIST rather than substring-matching each mode against the whole
// description is what makes this assertion real: "graph" and "search" both occur
// in the surrounding prose ("Persistent reasoning graph", "Search thoughts by
// composable filters"), so a bare containment check would pass for a mode the
// Modes line never named.
func recallModesNamedInDescription(t *testing.T, description string) []string {
	t.Helper()
	idx := strings.Index(description, "Modes:")
	require.GreaterOrEqual(t, idx, 0, "the tool description carries no 'Modes:' list for the recall arm")
	rest := description[idx+len("Modes:"):]
	end := strings.Index(rest, ".")
	require.GreaterOrEqual(t, end, 0, "the 'Modes:' list is unterminated — no closing period")

	var modes []string
	for part := range strings.SplitSeq(rest[:end], ",") {
		mode := strings.TrimSpace(strings.ReplaceAll(part, "(default)", ""))
		if mode != "" {
			modes = append(modes, strings.TrimSpace(mode))
		}
	}
	return sortedSet(modes)
}

// parseFuncFromFile parses one file and returns the named top-level func. It
// fails the test when the func is absent rather than returning nil, so a
// relocated dispatch surfaces as a broken anchor instead of an empty mode set.
func parseFuncFromFile(t *testing.T, path, name string) *ast.FuncDecl {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	require.NoErrorf(t, err, "parsing %s", path)
	for _, decl := range file.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if ok && fd.Name.Name == name && fd.Body != nil {
			return fd
		}
	}
	t.Fatalf("%s does not declare func %s — the census has lost its render anchor", path, name)
	return nil
}

// modeLiteralsCompared returns every string literal fd's body tests a `.Mode`
// selector against with ==. That is the shape every recall arm uses
// (`if a.Mode == "clusters"`), in either operand order.
func modeLiteralsCompared(fd *ast.FuncDecl) []string {
	var found []string
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		be, ok := n.(*ast.BinaryExpr)
		if !ok || be.Op != token.EQL {
			return true
		}
		for _, pair := range [][2]ast.Expr{{be.X, be.Y}, {be.Y, be.X}} {
			sel, isSel := pair[0].(*ast.SelectorExpr)
			if !isSel || sel.Sel.Name != "Mode" {
				continue
			}
			if val, isLit := stringLit(pair[1]); isLit {
				found = append(found, val)
			}
		}
		return true
	})
	return found
}

// switchCaseLiteralsOn returns every case-clause string literal of the switch in
// fd whose tag renders to the given expression.
func switchCaseLiteralsOn(fd *ast.FuncDecl, tag string) []string {
	var found []string
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		sw, ok := n.(*ast.SwitchStmt)
		if !ok || sw.Tag == nil || renderType(sw.Tag) != tag {
			return true
		}
		for _, stmt := range sw.Body.List {
			clause, isCase := stmt.(*ast.CaseClause)
			if !isCase {
				continue
			}
			for _, expr := range clause.List {
				if val, isLit := stringLit(expr); isLit {
					found = append(found, val)
				}
			}
		}
		return true
	})
	return found
}

// stringLitsAssignedTo returns every string literal assigned to the named local
// identifier in fd — the shape of the empty-mode default (`mode = "search"`).
func stringLitsAssignedTo(fd *ast.FuncDecl, name string) []string {
	var found []string
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for i, lhs := range as.Lhs {
			ident, isIdent := lhs.(*ast.Ident)
			if !isIdent || ident.Name != name || i >= len(as.Rhs) {
				continue
			}
			if val, isLit := stringLit(as.Rhs[i]); isLit {
				found = append(found, val)
			}
		}
		return true
	})
	return found
}

// valuesMissingFrom returns every member of want that `have` does not contain,
// sorted. It is the single instrument both directions of the census drive, which
// is what lets each of them prove it can report a non-zero on a holed input.
func valuesMissingFrom(have, want []string) []string {
	present := make(map[string]bool, len(have))
	for _, v := range have {
		present[v] = true
	}
	var missing []string
	for _, v := range want {
		if !present[v] {
			missing = append(missing, v)
		}
	}
	sort.Strings(missing)
	return missing
}

// withoutValue returns a copy of vals with every occurrence of drop removed.
func withoutValue(vals []string, drop string) []string {
	out := make([]string, 0, len(vals))
	for _, v := range vals {
		if v != drop {
			out = append(out, v)
		}
	}
	return out
}

// sortedSet dedupes and sorts, so every comparison above is set equality rather
// than order-sensitive.
func sortedSet(vals []string) []string {
	seen := make(map[string]bool, len(vals))
	out := make([]string, 0, len(vals))
	for _, v := range vals {
		if seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}
