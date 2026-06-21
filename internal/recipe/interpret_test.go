// SPDX-License-Identifier: Apache-2.0

package recipe

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

func parseOrFatal(t *testing.T, body string) *Recipe {
	t.Helper()
	r, err := Parse([]byte(body))
	require.NoError(t, err)
	return r
}

func recipeTargetSpec() TargetSpec {
	return TargetSpec{GraphType: kgtypes.GraphPractice, Name: "design-patterns"}
}

// twoSectionView seeds two section nodes wired by a relates-to edge so a
// select-emit-traverse-emit-link recipe can resolve cross-emit bindings.
func twoSectionView() *sourceView {
	s1 := &knowledgev1.Node{Id: "s1", Type: "section", SymbolName: "Message Router"}
	s2 := &knowledgev1.Node{Id: "s2", Type: "section", SymbolName: "Message Channel"}
	return &sourceView{
		byID:   map[string]*knowledgev1.Node{"s1": s1, "s2": s2},
		byType: map[string][]*knowledgev1.Node{"section": {s1, s2}},
		outEdges: map[string][]*knowledgev1.Edge{
			"s1": {{FromId: "s1", ToId: "s2", Type: "relates-to"}},
		},
		inEdges: map[string][]*knowledgev1.Edge{
			"s2": {{FromId: "s1", ToId: "s2", Type: "relates-to"}},
		},
	}
}

func TestInterpret_SelectEmit_StableIDsAndLineage(t *testing.T) {
	sv := twoSectionView()
	body := `select section
emit pattern {
    type := "pattern"
    name := section.symbol_name
}`
	recipe := parseOrFatal(t, body)
	target := recipeTargetSpec()
	const slug = "hohpe-eip"

	result, err := Interpret(context.Background(), recipe, sv, target, slug, Options{})
	require.NoError(t, err)

	require.Len(t, result.Nodes, 2, "one pattern per section row")
	require.Len(t, result.Lineage, 2, "one translated-from edge per emit")
	assert.Equal(t, 2, result.Stats.NodesEmitted)

	// StableID is deterministic over (target, slug, type, name).
	wantS1 := StableID(TargetKey(target), slug, "pattern", "Message Router")
	got := map[string]bool{}
	for _, n := range result.Nodes {
		got[n.Id] = true
		assert.Equal(t, "pattern", n.Type)
		assert.Equal(t, "recipe:"+slug, n.Source, "emitted node carries the recipe source tag")
	}
	assert.True(t, got[wantS1], "emitted node id must equal the StableID for the row")

	// Each lineage edge is a translated-from edge pointing target→source with
	// Evidence carrying the slug.
	for _, e := range result.Lineage {
		assert.Equal(t, kgtypes.EdgeTranslatedFrom, e.Type)
		assert.Equal(t, slug, SourceFromEvidence(e.Evidence))
		assert.True(t, e.ToID == "s1" || e.ToID == "s2", "lineage points back at a source node")
	}
}

func TestInterpret_CrossEmitLink_ResolvedInMemory(t *testing.T) {
	sv := twoSectionView()
	body := `select section
emit pattern {
    type := "pattern"
    name := section.symbol_name
} as $p
traverse relates-to out as $t
emit pattern {
    type := "pattern"
    name := $t.symbol_name
} as $rp
link $p --[relates-to]--> $rp`
	recipe := parseOrFatal(t, body)
	target := recipeTargetSpec()
	const slug = "hohpe-eip"

	result, err := Interpret(context.Background(), recipe, sv, target, slug, Options{})
	require.NoError(t, err)

	// The link rule resolved both endpoints from the in-run emitted set.
	require.Len(t, result.Edges, 1, "the cross-emit link must land exactly one edge")
	e := result.Edges[0]
	assert.Equal(t, kgtypes.EdgeType("relates-to"), e.Type)
	assert.Equal(t, "recipe", e.Method)
	assert.Equal(t, -1, e.FromIdx)
	assert.Equal(t, -1, e.ToIdx)
	// FromID is the $p emit's StableID (s1's pattern); ToID is the $rp emit's.
	assert.NotEmpty(t, e.FromID)
	assert.NotEmpty(t, e.ToID)
	assert.NotEqual(t, e.FromID, e.ToID)
	assert.Zero(t, result.Stats.LinkMisses, "both endpoints were emitted, so no miss")
}

func TestInterpret_DryRun_SameResultNoWrite(t *testing.T) {
	sv := twoSectionView()
	body := `select section
emit pattern {
    type := "pattern"
    name := section.symbol_name
}`
	recipe := parseOrFatal(t, body)
	// DryRun is honored by RunRecipe (skip the Sink write), not by Interpret —
	// the interpretation itself is identical either way. Assert the Result is
	// fully populated under DryRun.
	result, err := Interpret(context.Background(), recipe, sv, recipeTargetSpec(), "hohpe-eip", Options{DryRun: true})
	require.NoError(t, err)
	assert.Len(t, result.Nodes, 2)
	assert.Len(t, result.Lineage, 2)
}

func TestInterpret_LinkMiss_WhenEndpointNotEmitted(t *testing.T) {
	sv := twoSectionView()
	// $rp is never emitted (no second emit), so the link has an unbound
	// endpoint and silently skips with a LinkMiss.
	body := `select section
emit pattern {
    type := "pattern"
    name := section.symbol_name
} as $p
link $p --[relates-to]--> $rp`
	recipe := parseOrFatal(t, body)
	result, err := Interpret(context.Background(), recipe, sv, recipeTargetSpec(), "hohpe-eip", Options{})
	require.NoError(t, err)
	assert.Empty(t, result.Edges)
	assert.Equal(t, 2, result.Stats.LinkMisses, "one miss per source row with an unbound endpoint")
}

// emittedNames collects the SymbolName of every node a run emitted, for set
// assertions over the emitted node names.
func emittedNames(res *Result) []string {
	out := make([]string, 0, len(res.Nodes))
	for _, n := range res.Nodes {
		out = append(out, n.SymbolName)
	}
	return out
}

// TestInterpret_FilterRule_DropsRows pins evalFilter: a `~=` predicate keeps
// exactly the rows whose field matches and drops the rest, asserted by surviving
// node count AND name. A broken filter that keeps all or drops all fails here.
func TestInterpret_FilterRule_DropsRows(t *testing.T) {
	sv := twoSectionView() // "Message Router" + "Message Channel"
	body := `select section
filter section.symbol_name ~= /Router/
emit pattern {
    name := section.symbol_name
}`
	recipe := parseOrFatal(t, body)
	result, err := Interpret(context.Background(), recipe, sv, recipeTargetSpec(), "eip", Options{})
	require.NoError(t, err)

	require.Len(t, result.Nodes, 1, "only the Router section survives the filter")
	assert.Equal(t, "Message Router", result.Nodes[0].SymbolName)
	assert.Equal(t, 1, result.Stats.NodesEmitted)
}

// TestInterpret_GroupByRule_CollapsesByKey pins evalGroupBy: rows are collapsed
// to one representative per distinct key, the rep being the first row of each
// group. Two of the three sections share a "family" metadata value, so the
// recipe emits exactly two nodes — the first-seen section name per family.
func TestInterpret_GroupByRule_CollapsesByKey(t *testing.T) {
	a := &knowledgev1.Node{Id: "a", Type: "section", SymbolName: "Alpha", Metadata: map[string]string{"family": "routing"}}
	b := &knowledgev1.Node{Id: "b", Type: "section", SymbolName: "Bravo", Metadata: map[string]string{"family": "routing"}}
	c := &knowledgev1.Node{Id: "c", Type: "section", SymbolName: "Charlie", Metadata: map[string]string{"family": "channels"}}
	sv := &sourceView{
		byID:   map[string]*knowledgev1.Node{"a": a, "b": b, "c": c},
		byType: map[string][]*knowledgev1.Node{"section": {a, b, c}},
	}
	body := `select section
group_by node.metadata.family
emit pattern {
    name := section.symbol_name
}`
	recipe := parseOrFatal(t, body)
	result, err := Interpret(context.Background(), recipe, sv, recipeTargetSpec(), "eip", Options{})
	require.NoError(t, err)

	// One node per distinct family key (routing, channels) = 2.
	require.Len(t, result.Nodes, 2)
	assert.Equal(t, 2, result.Stats.NodesEmitted)
	// Representative is the FIRST row of each group: Alpha for routing (a before
	// b), Charlie for channels.
	assert.ElementsMatch(t, []string{"Alpha", "Charlie"}, emittedNames(result))
}

// TestInterpret_BindRule_PerRow is the explicit guard against the documented
// one-row-wins bug (interpret_select.go bind comment): each emitted node must
// carry its OWN lower(name), so the emitted-name SET equals the two distinct
// per-row derived values — NOT row-0's value twice.
func TestInterpret_BindRule_PerRow(t *testing.T) {
	sv := twoSectionView() // "Message Router" + "Message Channel"
	body := `select section
bind $slug := lower(section.symbol_name)
emit pattern {
    name := $slug
}`
	recipe := parseOrFatal(t, body)
	result, err := Interpret(context.Background(), recipe, sv, recipeTargetSpec(), "eip", Options{})
	require.NoError(t, err)

	require.Len(t, result.Nodes, 2)
	assert.ElementsMatch(t, []string{"message router", "message channel"}, emittedNames(result),
		"each row binds its own lower(name) — a broadcast bug would repeat row-0's value")
}

// TestInterpret_TraverseRule_OutExpands pins evalTraverse: an `out` traverse
// expands each row to its resolvable out-neighbors, and an edge to a node absent
// from byID is SKIPPED (orphan guard). a has rel→b, rel→c, and a dangling
// rel→ghost; only b and c emit. $t (the as-binding) is read in the emit, proving
// cloneRowVars carried it into each traverse-target row.
func TestInterpret_TraverseRule_OutExpands(t *testing.T) {
	a := &knowledgev1.Node{Id: "a", Type: "root", SymbolName: "A"}
	b := &knowledgev1.Node{Id: "b", Type: "leaf", SymbolName: "Bee"}
	c := &knowledgev1.Node{Id: "c", Type: "leaf", SymbolName: "Cee"}
	sv := &sourceView{
		byID:   map[string]*knowledgev1.Node{"a": a, "b": b, "c": c}, // ghost intentionally absent
		byType: map[string][]*knowledgev1.Node{"root": {a}, "leaf": {b, c}},
		outEdges: map[string][]*knowledgev1.Edge{
			"a": {
				{FromId: "a", ToId: "b", Type: "rel"},
				{FromId: "a", ToId: "c", Type: "rel"},
				{FromId: "a", ToId: "ghost", Type: "rel"}, // orphan — ghost not in byID
			},
		},
		inEdges: map[string][]*knowledgev1.Edge{
			"b": {{FromId: "a", ToId: "b", Type: "rel"}},
			"c": {{FromId: "a", ToId: "c", Type: "rel"}},
		},
	}
	body := `select root
traverse rel out as $t
emit pattern {
    name := $t
}`
	recipe := parseOrFatal(t, body)
	result, err := Interpret(context.Background(), recipe, sv, recipeTargetSpec(), "eip", Options{})
	require.NoError(t, err)

	// Exactly the two resolvable neighbors emit; the orphan ghost edge produced
	// nothing. $t resolves to each target's bound NodeID (b, c).
	require.Len(t, result.Nodes, 2, "two resolvable out-neighbors, orphan skipped")
	assert.ElementsMatch(t, []string{"b", "c"}, emittedNames(result),
		"the as-bound $t survived into the emit for each traverse target")
}

// TestInterpret_TraverseRule_InDirection pins the incoming direction: traverse
// `in` resolves to the FromId-side neighbor, and the as-bound var is readable in
// the subsequent emit. b--rel-->a, so traverse rel in from a yields b.
func TestInterpret_TraverseRule_InDirection(t *testing.T) {
	a := &knowledgev1.Node{Id: "a", Type: "root", SymbolName: "A"}
	b := &knowledgev1.Node{Id: "b", Type: "leaf", SymbolName: "Bee"}
	sv := &sourceView{
		byID:   map[string]*knowledgev1.Node{"a": a, "b": b},
		byType: map[string][]*knowledgev1.Node{"root": {a}, "leaf": {b}},
		outEdges: map[string][]*knowledgev1.Edge{
			"b": {{FromId: "b", ToId: "a", Type: "rel"}},
		},
		inEdges: map[string][]*knowledgev1.Edge{
			"a": {{FromId: "b", ToId: "a", Type: "rel"}},
		},
	}
	body := `select root
traverse rel in as $t
emit pattern {
    name := $t
}`
	recipe := parseOrFatal(t, body)
	result, err := Interpret(context.Background(), recipe, sv, recipeTargetSpec(), "eip", Options{})
	require.NoError(t, err)

	require.Len(t, result.Nodes, 1, "incoming traverse resolves to the FromId neighbor b")
	assert.Equal(t, "b", result.Nodes[0].SymbolName, "the as-bound $t (=b) drove the emit name")
}

// TestInterpret_BindRule_EmptyRows exercises evalBind's len(env.Rows)==0 branch:
// a bind appearing BEFORE any select runs against a nil row and lands the value
// on env.Vars. A subsequent select repopulates rows, and the emit reads $x via
// lookupVar's env fallback. Observed behavior (confirmed first-hand): both
// emitted nodes carry name "ab" — the env-scope binding survives into the emit.
func TestInterpret_BindRule_EmptyRows(t *testing.T) {
	sv := twoSectionView()
	body := `bind $x := concat("a", "b")
select section
emit pattern {
    name := $x
}`
	recipe := parseOrFatal(t, body)
	result, err := Interpret(context.Background(), recipe, sv, recipeTargetSpec(), "eip", Options{})
	require.NoError(t, err)

	require.Len(t, result.Nodes, 2, "both sections emit; the bind did not drop any row")
	for _, n := range result.Nodes {
		assert.Equal(t, "ab", n.SymbolName,
			"empty-rows bind landed on env.Vars and the emit resolved $x via env fallback")
	}
}

// oneSectionView seeds a single section node so a lookup/link recipe lands
// exactly one edge (one source row) rather than one-per-row.
func oneSectionView() *sourceView {
	s := &knowledgev1.Node{Id: "s1", Type: "section", SymbolName: "Message Router"}
	return &sourceView{
		byID:   map[string]*knowledgev1.Node{"s1": s},
		byType: map[string][]*knowledgev1.Node{"section": {s}},
	}
}

// TestInterpret_LookupRule_ResolvedAndLinks pins evalLookup's resolve path: the
// lookup recomputes the SAME StableID the prior emit produced (same identity),
// so emitted[id] is true → LookupsResolved increments and the dependent link
// lands exactly one edge.
func TestInterpret_LookupRule_ResolvedAndLinks(t *testing.T) {
	sv := oneSectionView()
	body := `select section
emit pattern {
    name := section.symbol_name
} as $p
lookup pattern by section.symbol_name as $q
link $p --[relates-to]--> $q`
	recipe := parseOrFatal(t, body)
	result, err := Interpret(context.Background(), recipe, sv, recipeTargetSpec(), "eip", Options{})
	require.NoError(t, err)

	assert.Positive(t, result.Stats.LookupsResolved, "lookup recomputed an already-emitted StableID")
	assert.Zero(t, result.Stats.LookupMisses)
	require.Len(t, result.Edges, 1, "the resolved lookup binding fed exactly one link edge")
}

// TestInterpret_LookupRule_Miss pins the not-emitted branch: a lookup identity
// that was never emitted increments LookupMisses, leaves $q unbound, and the
// following link is skipped (no edge).
func TestInterpret_LookupRule_Miss(t *testing.T) {
	sv := oneSectionView()
	// The lookup identity (name + "-X") was never emitted, so its StableID is
	// absent from the in-run emitted set.
	body := `select section
emit pattern {
    name := section.symbol_name
} as $p
lookup pattern by concat(section.symbol_name, "-X") as $q
link $p --[relates-to]--> $q`
	recipe := parseOrFatal(t, body)
	result, err := Interpret(context.Background(), recipe, sv, recipeTargetSpec(), "eip", Options{})
	require.NoError(t, err)

	assert.Positive(t, result.Stats.LookupMisses, "the unemitted identity is a lookup miss")
	assert.Empty(t, result.Edges, "the unbound $q endpoint skips the link")
}

// TestInterpret_LookupRule_EmptyIdentity pins the empty-identity branch
// (interpret_emit.go evalLookup): a lookup whose identity expr resolves to ""
// increments LookupMisses without computing a StableID.
func TestInterpret_LookupRule_EmptyIdentity(t *testing.T) {
	sv := oneSectionView()
	// node.metadata.nope resolves to "" (the section has no metadata), so the
	// lookup identity is empty.
	body := `select section
emit pattern {
    name := section.symbol_name
} as $p
lookup pattern by node.metadata.nope as $q`
	recipe := parseOrFatal(t, body)
	result, err := Interpret(context.Background(), recipe, sv, recipeTargetSpec(), "eip", Options{})
	require.NoError(t, err)

	assert.Positive(t, result.Stats.LookupMisses, "empty identity is a lookup miss")
	assert.Zero(t, result.Stats.LookupsResolved)
}

// TestInterpret_SourceRef_OverridesAnchor pins evalSourceRef → evalEmit: after a
// source_ref, every emitted lineage edge's ToID is the overridden anchor value,
// NOT the source row's NodeID.
func TestInterpret_SourceRef_OverridesAnchor(t *testing.T) {
	sv := twoSectionView()
	body := `select section
source_ref "custom-anchor"
emit pattern {
    name := section.symbol_name
}`
	recipe := parseOrFatal(t, body)
	result, err := Interpret(context.Background(), recipe, sv, recipeTargetSpec(), "eip", Options{})
	require.NoError(t, err)

	require.Len(t, result.Lineage, 2)
	for _, e := range result.Lineage {
		assert.Equal(t, "custom-anchor", e.ToID,
			"lineage anchors to the source_ref override, not the row NodeID")
	}
}
