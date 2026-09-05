// SPDX-License-Identifier: Apache-2.0

package engine

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// resolveFixture is a graph fixture that carries a VOCABULARY as well as data.
// That is the substantive difference from the package's other ExecuteFn fakes
// (there are nine, and none of them serves Stats at all): edge-type resolution
// reads the target graph's edge vocabulary through a StatsFn before any plan is
// compiled, so a fixture that only answers Execute cannot exercise the seam.
//
// The vocabulary is DERIVED from the fixture's own edges rather than declared
// beside them, so the vocabulary and the data can never disagree — a fixture
// whose vocabulary is hand-written is an answer key the test supplies to itself.
type resolveFixture struct {
	edges []*knowledgev1.Edge
	// statsCalls counts vocabulary reads. It is load-bearing TWICE OVER: the
	// cost legs assert on it directly, and it is the only observable in the
	// whole suite that can see WHERE resolution sits inside Dispatch. Correct
	// output is produced by several wrong placements; only the read count
	// separates them.
	statsCalls int
	// statsErr, when seeded, makes the graph unstattable — the case where the
	// caller must be told rather than served a degraded walk.
	statsErr error
	// writes records the relationship of every mutation that reached exec, so a
	// refusal can be distinguished from a write that happened with the wrong
	// spelling.
	writes []string
}

// vocab derives EdgesByType by counting the fixture's own edges.
func (g *resolveFixture) vocab() map[string]int64 {
	v := make(map[string]int64, len(g.edges))
	for _, e := range g.edges {
		v[e.GetType()]++
	}
	return v
}

// stats is the fixture's StatsFn seam. It increments statsCalls on EVERY call,
// including the seeded-error call, because a failed vocabulary read still costs
// a round trip and a cost claim that ignored failures would be measuring the
// wrong thing.
func (g *resolveFixture) stats(_ context.Context, _ *knowledgev1.StatsRequest) (*knowledgev1.StatsResponse, error) {
	g.statsCalls++
	if g.statsErr != nil {
		return nil, g.statsErr
	}
	return &knowledgev1.StatsResponse{
		GraphStats: &knowledgev1.GraphStats{EdgesByType: g.vocab()},
	}, nil
}

// matches is an EXACT string comparison with no folding of any kind.
//
// It is a DELIBERATE MIRROR of the store, with the source cited: the server's
// edgeTypeFilter.matches (cmd/knowledge-server/internal/store/edge_iterator.go,
// func matches — set/slice membership, no case handling) and applySelection
// (cmd/knowledge-server/internal/bootstrap/engine_decode.go, which documents
// that "edge_types are applied AS-GIVEN" and passes each token straight to
// q.Edge). The client cannot import either — cmd/knowledge has no path across
// the server's internal boundary — so the comparison is re-declared here with
// its source named, the same justification acceptance_test.go records for
// re-declaring a backend across a package boundary.
//
// The mirror is what makes these tests mean anything: if this folded, every leg
// below would pass against a client that also folded, and the suite would be
// agreeing with the implementation rather than with the store.
func (g *resolveFixture) matches(want []string, have string) bool {
	if len(want) == 0 {
		return true // an empty filter accepts every type, as the store's does.
	}
	return slices.Contains(want, have)
}

// exec is the fixture's ExecuteFn seam.
//
// It serves exactly two shapes and NO node-enumeration arm. The omission is
// deliberate and load-bearing: a start-less traverse that reaches exec at all
// enumerates zero nodes and therefore renders a graph-wide body reading
// "- nodes: 0". That rendered body is what lets the start-less placement leg
// tell resolution-before-dispatchGraphWideEdges from resolution-after.
func (g *resolveFixture) exec(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	if m := req.GetMutation(); m != nil {
		spec := m.GetEdgeSpec()
		g.writes = append(g.writes, spec.GetRelationship())
		if m.GetKind() == knowledgev1.MutationPlan_MUTATION_KIND_LINK {
			return &knowledgev1.ExecuteResponse{AffectedCount: 1}, nil
		}
		// UNLINK: edge identity is from/to/type and the type comparison is
		// EXACT, the same mirror matches() documents — so an unlink naming a
		// type the graph does not store removes nothing, which is what makes
		// the idempotence leg below observable rather than asserted.
		from := ""
		if ids := m.GetSelection().GetIds(); len(ids) > 0 {
			from = ids[0]
		}
		var affected int64
		for _, e := range g.edges {
			if e.GetFromId() == from && e.GetToId() == spec.GetToId() && e.GetType() == spec.GetRelationship() {
				affected = 1
			}
		}
		return &knowledgev1.ExecuteResponse{AffectedCount: affected}, nil
	}
	sel := req.GetQuery().GetSelection()
	from := ""
	if ids := sel.GetFromId(); len(ids) > 0 {
		from = ids[0]
	}
	var results []*knowledgev1.TraversalResult
	for _, e := range g.edges {
		if e.GetFromId() != from {
			continue
		}
		if !g.matches(sel.GetEdgeTypes(), e.GetType()) {
			continue
		}
		results = append(results, &knowledgev1.TraversalResult{
			Distance: 1,
			Node: &knowledgev1.Node{
				Id:         e.GetToId(),
				Type:       "finding",
				SymbolName: e.GetToId(),
			},
		})
	}
	return &knowledgev1.ExecuteResponse{TraversalResults: results}, nil
}

// edgeOf builds one fixture edge.
func edgeOf(from, to, typ string) *knowledgev1.Edge {
	return &knowledgev1.Edge{FromId: from, ToId: to, Type: typ}
}

// twoFamilyFixture is ONE graph carrying TWO casing families at once, plus a
// third unrelated type.
//
// This shape is the whole reason a per-graph fold cannot be correct: no function
// of the graph NAME can answer whether "contains" or "CONTAINS" is the right
// spelling here, because both are stored. It also gives the vocabulary listing
// in a refusal something to show — "references" is the third token every
// unknown-spelling refusal must name, and a message that degraded to
// near-misses only would drop it.
func twoFamilyFixture() *resolveFixture {
	return &resolveFixture{edges: []*knowledgev1.Edge{
		edgeOf("root", "upper-child", "CONTAINS"),
		edgeOf("root", "lower-child", "contains"),
		edgeOf("root", "ref", "references"),
	}}
}

// dispatchOn runs one tool call through Dispatch against the fixture and
// returns the rendered text plus the IsError flag.
//
// It returns IsError rather than requiring success because most legs below
// assert a REFUSAL, and a helper that required success could not express them.
//
// It joins EVERY text block rather than reading only the first: an
// unmatched-spelling unlink notice rides as a separate content block (so a
// format=json payload stays independently parseable, the disposition
// WithTruncationNoticeFor documents), and the joined form is what a caller
// actually receives.
func dispatchOn(t *testing.T, g *resolveFixture, tool, args string) (string, bool) {
	t.Helper()
	out, err := Dispatch(context.Background(), g.exec, g.stats, tool, json.RawMessage(args))
	require.NoError(t, err, "a refusal is rendered as an error RESULT, never returned as a Go error")
	var sb strings.Builder
	for _, block := range out.Content {
		if block.Type != "text" {
			continue
		}
		sb.WriteString(block.Text)
		sb.WriteString("\n")
	}
	return sb.String(), out.IsError
}

// TestResolveEdgeTypes_TraverseProperty pins the READ half of the four rules,
// plus the two PLACEMENT legs.
func TestResolveEdgeTypes_TraverseProperty(t *testing.T) {
	t.Run("an exact match is served, and serves only its own family", func(t *testing.T) {
		g := twoFamilyFixture()
		body, isErr := dispatchOn(t, g, "traverse",
			`{"graph":"pdf","name":"bk","start":"root","edge_types":["CONTAINS"]}`)
		require.False(t, isErr, "an exactly-stored spelling is served: %s", body)
		assert.Contains(t, body, "upper-child")
		assert.NotContains(t, body, "lower-child",
			"CONTAINS must not sweep in the lowercase family — the store compares exactly")
	})

	t.Run("the other exact match is served, and serves only its own family", func(t *testing.T) {
		g := twoFamilyFixture()
		body, isErr := dispatchOn(t, g, "traverse",
			`{"graph":"pdf","name":"bk","start":"root","edge_types":["contains"]}`)
		require.False(t, isErr, "the lowercase family is stored too and answers its own spelling: %s", body)
		assert.Contains(t, body, "lower-child")
		assert.NotContains(t, body, "upper-child")
	})

	t.Run("a unique case-insensitive match resolves to the STORED spelling", func(t *testing.T) {
		// The graph stores CALLS; the caller writes "calls". Rule 2 rewrites the
		// caller's spelling to the one the store holds, which is what keeps
		// edge_types ["calls"] working against a code graph.
		// The excluded node is named so its name is not a SUBSTRING of the
		// render's own vocabulary — a shorter name like "dep" is contained in
		// the "at depth 1" every traversal row prints, and the assertion below
		// would then fail against a correct implementation.
		g := &resolveFixture{edges: []*knowledgev1.Edge{
			edgeOf("root", "callee", "CALLS"),
			edgeOf("root", "dependency-target", "DEPENDS_ON"),
		}}
		body, isErr := dispatchOn(t, g, "traverse",
			`{"graph":"code","repo":"r","start":"root","edge_types":["calls"]}`)
		require.False(t, isErr, "a unique fold resolves rather than refusing: %s", body)
		assert.Contains(t, body, "callee")
		assert.NotContains(t, body, "dependency-target", "resolution must not widen the filter")
	})

	t.Run("an ambiguous fold REFUSES naming both stored spellings", func(t *testing.T) {
		g := twoFamilyFixture()
		body, isErr := dispatchOn(t, g, "traverse",
			`{"graph":"pdf","name":"bk","start":"root","edge_types":["Contains"]}`)
		require.True(t, isErr, "an ambiguous spelling is refused, never guessed: %s", body)
		assert.Contains(t, body, "Contains", "the refusal names the caller's spelling")
		assert.Contains(t, body, "CONTAINS", "the refusal names the first stored spelling")
		assert.Contains(t, body, "contains", "the refusal names the second stored spelling")
		assert.NotContains(t, body, "No nodes reached.",
			"a refusal must never degrade into an empty-looking successful walk")
	})

	t.Run("an unknown spelling REFUSES naming the whole vocabulary", func(t *testing.T) {
		g := twoFamilyFixture()
		body, isErr := dispatchOn(t, g, "traverse",
			`{"graph":"pdf","name":"bk","start":"root","edge_types":["MENTIONS"]}`)
		require.True(t, isErr, "an unknown spelling is refused on a READ: %s", body)
		assert.Contains(t, body, "MENTIONS", "the refusal names the caller's spelling")
		assert.Contains(t, body, "CONTAINS")
		assert.Contains(t, body, "contains")
		// Asserted EXPLICITLY: "references" case-insensitively resembles nothing
		// the caller wrote, so a message that degraded to near-misses only would
		// still contain the two contains spellings and pass without this line.
		assert.Contains(t, body, "references",
			"the refusal lists the graph's WHOLE vocabulary, not just the near-misses")
		assert.NotContains(t, body, "No nodes reached.")
	})

	t.Run("the knowledge lowercase control still resolves", func(t *testing.T) {
		g := &resolveFixture{edges: []*knowledgev1.Edge{
			edgeOf("root", "kid", "contains"),
			edgeOf("root", "peer", "relates-to"),
		}}
		body, isErr := dispatchOn(t, g, "traverse",
			`{"start":"root","edge_types":["relates-to"]}`)
		require.False(t, isErr, "the ordinary knowledge-graph case is unchanged: %s", body)
		assert.Contains(t, body, "peer")
		assert.NotContains(t, body, "kid")
	})

	t.Run("an invalid direction is reported as such and costs NO vocabulary read", func(t *testing.T) {
		// PLACEMENT LEG. This is what catches resolution hoisted ABOVE
		// precheckTraverse: a request that is invalid on its face must be
		// refused for the reason it is invalid, and must not buy a vocabulary
		// read on the way. Only statsCalls can see the second half.
		g := twoFamilyFixture()
		body, isErr := dispatchOn(t, g, "traverse",
			`{"graph":"pdf","name":"bk","start":"root","direction":"sideways","edge_types":["MENTIONS"]}`)
		require.True(t, isErr, "an unknown direction is refused: %s", body)
		assert.Contains(t, body, "sideways", "the refusal names the direction fault")
		assert.NotContains(t, body, "edge_types",
			"the reported fault is the direction, not the edge types")
		assert.Equal(t, 0, g.statsCalls,
			"an invalid request must not cost a vocabulary read — resolution belongs AFTER precheckTraverse")
	})

	t.Run("a START-LESS graph-wide traverse resolves through the same seam", func(t *testing.T) {
		// PLACEMENT LEG. This is what catches resolution placed even ONE
		// STATEMENT below dispatchGraphWideEdges: that arm has its own
		// edge_types path (dispatch_graphwide.go), so an unresolved start-less
		// traverse renders a plausible graph-wide body instead of refusing —
		// which is the ticket's own silent-zero defect.
		g := twoFamilyFixture()
		body, isErr := dispatchOn(t, g, "traverse",
			`{"graph":"pdf","name":"bk","edge_types":["MENTIONS"]}`)
		require.True(t, isErr, "the start-less shape refuses an unknown spelling too: %s", body)
		assert.Contains(t, body, "MENTIONS", "the refusal names the caller's spelling")
		assert.Contains(t, body, "CONTAINS", "the refusal names the vocabulary")
		assert.NotContains(t, body, "nodes:",
			"a refused start-less traverse must never render a graph-wide body")
		assert.Equal(t, 1, g.statsCalls,
			"the start-less arm reads the vocabulary through the same seam, exactly once")
	})
}

// TestResolveEdgeTypes_Cost pins the cost contract: one vocabulary read per
// CALL, and none at all when the caller names no edge types.
func TestResolveEdgeTypes_Cost(t *testing.T) {
	t.Run("three edge types cost exactly ONE vocabulary read", func(t *testing.T) {
		g := twoFamilyFixture()
		_, isErr := dispatchOn(t, g, "traverse",
			`{"graph":"pdf","name":"bk","start":"root","edge_types":["CONTAINS","contains","references"]}`)
		require.False(t, isErr)
		assert.Equal(t, 1, g.statsCalls,
			"the vocabulary is fetched once and every spelling is resolved against that one map")
	})

	t.Run("a traverse naming no edge_types costs ZERO vocabulary reads", func(t *testing.T) {
		g := twoFamilyFixture()
		body, isErr := dispatchOn(t, g, "traverse", `{"graph":"pdf","name":"bk","start":"root"}`)
		require.False(t, isErr, "%s", body)
		assert.Equal(t, 0, g.statsCalls,
			"an unfiltered traverse costs exactly what it costs today")
		assert.Contains(t, body, "upper-child", "the unfiltered walk still returns every family")
		assert.Contains(t, body, "lower-child")
	})
}

// TestResolveEdgeTypes_UnstattableGraphSurfacesTheError pins the loud-failure
// rule: a graph whose vocabulary cannot be read is reported, never degraded
// into an unfiltered or empty walk.
func TestResolveEdgeTypes_UnstattableGraphSurfacesTheError(t *testing.T) {
	g := twoFamilyFixture()
	g.statsErr = errors.New("stats backend unavailable")
	body, isErr := dispatchOn(t, g, "traverse",
		`{"graph":"pdf","name":"bk","start":"root","edge_types":["CONTAINS"]}`)
	require.True(t, isErr, "an unstattable graph is an error, not a silent pass-through: %s", body)
	assert.Contains(t, body, "cannot read the edge vocabulary")
	assert.Contains(t, body, "stats backend unavailable", "the underlying cause is carried, not swallowed")
	assert.NotContains(t, body, "No nodes reached.",
		"degrading to an empty walk would hide the failure behind a plausible answer")
}

// TestResolveEdgeTypes_LinkProperty pins the WRITE half of the four rules.
//
// Rules 1-3 are identical to the read path. Rule 4 DIVERGES by design: a read
// resolves against what EXISTS, because that is the entire question a filter
// asks, while a write may introduce what SHOULD exist. The read-side control
// leg below drives the same spelling down both paths in one test so the
// asymmetry is observed rather than asserted.
func TestResolveEdgeTypes_LinkProperty(t *testing.T) {
	linkArgs := func(graph, rel string) string {
		return `{"operation":"link","graph":"` + graph + `","from":"a","to":"b","relationship":"` + rel + `"}`
	}

	t.Run("a unique case-insensitive match writes the STORED spelling", func(t *testing.T) {
		g := &resolveFixture{edges: []*knowledgev1.Edge{edgeOf("x", "y", "contains")}}
		body, isErr := dispatchOn(t, g, "mutate", linkArgs("pdf", "CONTAINS"))
		require.False(t, isErr, "%s", body)
		assert.Equal(t, []string{"contains"}, g.writes,
			"linking CONTAINS into a graph carrying only contains must ADOPT contains — this is the ticket's defect")
	})

	t.Run("an exact match writes byte-exact", func(t *testing.T) {
		g := &resolveFixture{edges: []*knowledgev1.Edge{edgeOf("x", "y", "CONTAINS")}}
		body, isErr := dispatchOn(t, g, "mutate", linkArgs("pdf", "CONTAINS"))
		require.False(t, isErr, "%s", body)
		assert.Equal(t, []string{"CONTAINS"}, g.writes)
	})

	t.Run("a mis-cased write adopts the existing spelling, upper into lower", func(t *testing.T) {
		g := &resolveFixture{edges: []*knowledgev1.Edge{edgeOf("x", "y", "mentions")}}
		body, isErr := dispatchOn(t, g, "mutate", linkArgs("pdf", "MENTIONS"))
		require.False(t, isErr, "%s", body)
		assert.Equal(t, []string{"mentions"}, g.writes)
	})

	t.Run("a mis-cased write adopts the existing spelling, lower into upper", func(t *testing.T) {
		// The reverse direction, which a per-graph fold could never satisfy:
		// the same graph family would have folded this one the other way.
		g := &resolveFixture{edges: []*knowledgev1.Edge{edgeOf("x", "y", "MENTIONS")}}
		body, isErr := dispatchOn(t, g, "mutate", linkArgs("pdf", "mentions"))
		require.False(t, isErr, "%s", body)
		assert.Equal(t, []string{"MENTIONS"}, g.writes)
	})

	t.Run("the FIRST edge of a new type into an EMPTY graph is written", func(t *testing.T) {
		// BOOTSTRAP LEG. An empty vocabulary is not a special case in the
		// resolver — it simply produces no match, which a write admits. If a
		// write refused here, no graph could ever get its first edge.
		g := &resolveFixture{}
		body, isErr := dispatchOn(t, g, "mutate", linkArgs("linkage", "BUILDS"))
		require.False(t, isErr, "the first edge of an empty graph must be writable: %s", body)
		assert.Equal(t, []string{"BUILDS"}, g.writes,
			"the caller's spelling becomes the new family")
		assert.Equal(t, 1, g.statsCalls)
	})

	t.Run("a new type is admitted alongside existing ones", func(t *testing.T) {
		g := &resolveFixture{edges: []*knowledgev1.Edge{edgeOf("x", "y", "CONTAINS")}}
		body, isErr := dispatchOn(t, g, "mutate", linkArgs("pdf", "MENTIONS"))
		require.False(t, isErr, "%s", body)
		assert.Equal(t, []string{"MENTIONS"}, g.writes,
			"a graph with a vocabulary can still gain a second family")
	})

	t.Run("the READ side refuses the very spelling the WRITE admits", func(t *testing.T) {
		// The asymmetry, observed in ONE test rather than inferred from two.
		g := &resolveFixture{edges: []*knowledgev1.Edge{edgeOf("x", "y", "CONTAINS")}}
		wbody, wErr := dispatchOn(t, g, "mutate", linkArgs("pdf", "MENTIONS"))
		require.False(t, wErr, "the write admits the unknown spelling: %s", wbody)
		require.Equal(t, []string{"MENTIONS"}, g.writes)

		rbody, rErr := dispatchOn(t, g, "traverse",
			`{"graph":"pdf","start":"x","edge_types":["MENTIONS"]}`)
		require.True(t, rErr, "the read refuses the identical spelling: %s", rbody)
		assert.Contains(t, rbody, "CONTAINS", "and names the vocabulary when it does")
	})

	t.Run("an ambiguous relationship REFUSES and writes nothing", func(t *testing.T) {
		g := twoFamilyFixture()
		body, isErr := dispatchOn(t, g, "mutate", linkArgs("pdf", "Contains"))
		require.True(t, isErr, "an ambiguous spelling is refused on the write path too: %s", body)
		assert.Contains(t, body, "CONTAINS")
		assert.Contains(t, body, "contains")
		assert.Empty(t, g.writes, "a refused write must not reach exec")
	})

	t.Run("an unlink of an unmatched spelling removes nothing and names the vocabulary", func(t *testing.T) {
		// ORCHESTRATOR-RULED (2026-09-02). unlink KEEPS the declaration path, so
		// it stays idempotent as the mutate tool documents — it does not error.
		// But the standing rule that an unmatched spelling is reported with the
		// vocabulary named still holds, so the response carries a notice.
		g := &resolveFixture{edges: []*knowledgev1.Edge{edgeOf("x", "y", "CONTAINS")}}
		body, isErr := dispatchOn(t, g, "mutate",
			`{"operation":"unlink","graph":"pdf","from":"x","to":"y","relationship":"MENTIONS"}`)
		require.False(t, isErr, "an unlink of an unmatched spelling is not an error: %s", body)
		assert.Contains(t, body, "MENTIONS", "the notice names the unmatched spelling")
		assert.Contains(t, body, "CONTAINS", "the notice names the graph's vocabulary")
		assert.Contains(t, body, "0 edges removed", "the notice states that nothing was removed")
	})

	t.Run("an unlink of a unique-fold spelling removes the stored-spelling edge", func(t *testing.T) {
		g := &resolveFixture{edges: []*knowledgev1.Edge{edgeOf("x", "y", "contains")}}
		body, isErr := dispatchOn(t, g, "mutate",
			`{"operation":"unlink","graph":"pdf","from":"x","to":"y","relationship":"CONTAINS"}`)
		require.False(t, isErr, "%s", body)
		assert.Equal(t, []string{"contains"}, g.writes,
			"the unlink targets the spelling the graph actually stores")
		assert.NotContains(t, body, "0 edges removed",
			"a resolved unlink carries no unmatched-spelling notice")
	})

	t.Run("the knowledge lowercase control still writes", func(t *testing.T) {
		g := &resolveFixture{edges: []*knowledgev1.Edge{edgeOf("x", "y", "relates-to")}}
		body, isErr := dispatchOn(t, g, "mutate", linkArgs("", "relates-to"))
		require.False(t, isErr, "the ordinary knowledge-graph write is unchanged: %s", body)
		assert.Equal(t, []string{"relates-to"}, g.writes)
	})
}
