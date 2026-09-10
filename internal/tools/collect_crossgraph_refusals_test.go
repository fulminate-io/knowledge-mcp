// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/externalcollector"
)

// collect_crossgraph_refusals_test.go — R3's three refusals at the collect
// level, and the resolution pass driven directly for the arms a whole collect
// cannot reach (the refusal ORDER, the empty edge list, the malformed edge, the
// degraded client).
//
// Split from collect_crossgraph_test.go, which keeps the fixtures every test
// here uses and the R2 transition rows, so both files stay inside the
// repository's 500-line file gate. The fixtures are package-level and shared;
// nothing was duplicated.

// --- R3: the three refusals, each with a same-run positive control ---

// TestCustomCollect_RefusesBeforeTheResolver drives each R3 condition and its
// control through one collect each.
//
// EACH ROW NEEDS ITS CONTROL because each asserts an ERROR where the shared
// resolver's own behavior is SILENCE: ResolveAndLink writes into an unvalidated
// target graph, best-effort-links an endpoint it cannot find, and returns a nil
// error on a failed enumeration. A row asserting only the error cannot tell a
// working refusal from a collect that never reached the pass at all.
func TestCustomCollect_RefusesBeforeTheResolver(t *testing.T) {
	for _, tc := range []struct {
		name       string
		seedTarget bool
		seedNode   bool
		execErr    bool
		wantParts  []string
	}{
		{
			name:       "the named graph does not exist",
			seedTarget: false, seedNode: false,
			wantParts: []string{crossTargetFamily, "ISSUE-1", crossTargetNode, "tracked_by", "no graph"},
		},
		{
			name:       "the endpoint is not in the named graph",
			seedTarget: true, seedNode: false,
			wantParts: []string{crossTargetFamily, "ISSUE-1", crossTargetNode, "tracked_by", "not found"},
		},
		{
			name:       "the enumeration failed",
			seedTarget: true, seedNode: true, execErr: true,
			wantParts: []string{crossTargetFamily, "could not be enumerated"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deps, recorder, sink := crossGraphFixture(t,
				crossGraphPayload(crossTargetFamily, crossTargetNode), tc.seedTarget, tc.seedNode)
			if tc.execErr {
				recorder.execErr = assertAnError{}
			}

			handled, res := callCollect(deps, `{"type":"`+shadowStubType+`","id":"own-graph"}`)
			require.True(t, handled)
			require.True(t, res.IsError, "the collect must FAIL: %s", resultText(res))
			for _, part := range tc.wantParts {
				assert.Contains(t, resultText(res), part,
					"the refusal must name the edge and the condition")
			}
			assert.Nil(t, sink.last(),
				"a refused cross-graph edge fails the collect BEFORE the chunk write, so nothing is shipped")
			assert.Empty(t, linkageMutations(recorder),
				"the refusal is ahead of the resolver: no partial linkage work is done")
		})
	}

	t.Run("CONTROL: the same collect with the graph, the endpoint and the enumeration all present", func(t *testing.T) {
		deps, recorder, sink := crossGraphFixture(t,
			crossGraphPayload(crossTargetFamily, crossTargetNode), true, true)
		handled, res := callCollect(deps, `{"type":"`+shadowStubType+`","id":"own-graph"}`)
		require.True(t, handled)
		require.False(t, res.IsError, resultText(res))
		assert.NotNil(t, sink.last(), "the control collect ships its own graph")
		assert.NotEmpty(t, linkageMutations(recorder), "the control collect reaches linkage")
	})
}

// TestCustomCollect_RefusesASourceGraphEdgeBeforeTheResolver is the mirror of
// the rows above for the field whose FOREIGN endpoint is the FROM. It drives the
// two conditions a source-graph edge can fail on — the family has no loaded
// graph, and the FROM is in none of that family's graphs — each with the same
// control the target-graph rows carry.
//
// THE ENDPOINT NAMED IN THE REFUSAL IS THE ONE THAT WAS LOOKED FOR, and that is
// what the second row pins: a pass that widened the family read but kept
// locating ToID would report the collect's OWN node id as the endpoint it could
// not find, sending a collector author to look for a node that was never
// supposed to be foreign.
func TestCustomCollect_RefusesASourceGraphEdgeBeforeTheResolver(t *testing.T) {
	for _, tc := range []struct {
		name       string
		seedTarget bool
		seedNode   bool
		wantParts  []string
	}{
		{
			name:       "the named source family has no loaded graph",
			seedTarget: false, seedNode: false,
			// The family is asserted INSIDE the condition's own clause, not merely
			// somewhere in the message: the edge description names the family too,
			// so a looser assertion passes on a pass that enumerated the family ""
			// and reported it as absent. Measured — that is exactly what the
			// mutation with the source arm removed produced.
			wantParts: []string{
				`no graph of family "` + crossTargetFamily + `" is loaded`,
				crossTargetNode, "ISSUE-1", "tracked_by", "source_graph",
			},
		},
		{
			name:       "the FROM is in none of that family's graphs",
			seedTarget: true, seedNode: false,
			wantParts: []string{
				`endpoint "` + crossTargetNode + `" was not found in any "` + crossTargetFamily + `" graph`,
				"source_graph",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deps, recorder, sink := crossGraphFixture(t,
				sourceGraphPayload(), tc.seedTarget, tc.seedNode)

			handled, res := callCollect(deps, `{"type":"`+shadowStubType+`","id":"own-graph"}`)
			require.True(t, handled)
			require.True(t, res.IsError, "the collect must FAIL: %s", resultText(res))
			for _, part := range tc.wantParts {
				assert.Contains(t, resultText(res), part,
					"the refusal must name the edge, the field that named the family, and the condition")
			}
			assert.Nil(t, sink.last(),
				"a refused cross-graph edge fails the collect BEFORE the chunk write, so nothing is shipped")
			assert.Empty(t, linkageMutations(recorder),
				"the refusal is ahead of the resolver: no partial linkage work is done")
		})
	}

	t.Run("CONTROL: the same source-graph collect with the family loaded and holding the FROM", func(t *testing.T) {
		deps, recorder, sink := crossGraphFixture(t,
			sourceGraphPayload(), true, true)
		handled, res := callCollect(deps, `{"type":"`+shadowStubType+`","id":"own-graph"}`)
		require.True(t, handled)
		require.False(t, res.IsError, resultText(res))
		assert.NotNil(t, sink.last(), "the control collect ships its own graph")
		assert.NotEmpty(t, linkageMutations(recorder), "the control collect reaches linkage")
	})
}

// TestCustomCollect_RefusesAnEdgeNamingBothFamilies is the refusal the two
// fields make necessary, and it is STRUCTURAL rather than tidiness: one
// resolution enumerates ONE family and carries ONE scan list into the composer,
// so an edge foreign at both ends names a relationship this path cannot resolve.
// Admitting it would resolve one end against the other end's family and link the
// remaining endpoint by its raw id, silently.
func TestCustomCollect_RefusesAnEdgeNamingBothFamilies(t *testing.T) {
	bothPayload := map[string]any{
		"nodes": []any{map[string]any{"id": "ISSUE-1", "type": "issue"}},
		"edges": []any{map[string]any{
			"from_id": crossTargetNode, "to_id": crossSecondNode, "type": "tracked_by",
			"source_graph": crossTargetFamily, "target_graph": crossSecondFamily,
		}},
		"walk_complete": true,
	}
	deps, recorder, sink := crossGraphFixture(t, bothPayload, true, true)

	handled, res := callCollect(deps, `{"type":"`+shadowStubType+`","id":"own-graph"}`)
	require.True(t, handled)
	require.True(t, res.IsError, "the collect must FAIL: %s", resultText(res))
	for _, part := range []string{crossTargetFamily, crossSecondFamily, crossTargetNode, crossSecondNode, "both"} {
		assert.Contains(t, resultText(res), part,
			"the refusal names the edge and BOTH families, so the author sees which two")
	}
	assert.Nil(t, sink.last(), "the refusal precedes the chunk write")
	assert.Empty(t, linkageMutations(recorder), "and precedes every linkage write")

	// SAME-RUN CONTROL: the identical collect with only source_graph set
	// succeeds, so the refusal above is caused by the second field rather than by
	// anything else in the payload.
	okDeps, okRecorder, okSink := crossGraphFixture(t,
		sourceGraphPayload(), true, true)
	okHandled, okRes := callCollect(okDeps, `{"type":"`+shadowStubType+`","id":"own-graph"}`)
	require.True(t, okHandled)
	require.False(t, okRes.IsError, resultText(okRes))
	assert.NotNil(t, okSink.last())
	assert.NotEmpty(t, linkageMutations(okRecorder))
}

// assertAnError is a distinct error type for the injected Execute failure, so
// the enumeration arm's message is attributable to the injection rather than to
// any error the fixture might raise on its own.
type assertAnError struct{}

func (assertAnError) Error() string { return "ful1798-injected-execute-failure" }

// TestResolveCrossGraphEdges_RefusalOrderIsGraphThenEndpoint pins the ORDER R3
// states, which the collect-level rows above cannot distinguish: with BOTH the
// graph absent and the endpoint absent, the refusal must name the graph, since
// "not found in a graph that does not exist" is the less useful of the two
// answers.
func TestResolveCrossGraphEdges_RefusalOrderIsGraphThenEndpoint(t *testing.T) {
	deps, _, _ := crossGraphFixture(t, crossGraphPayload("", ""), false, false)
	err := resolveCrossGraphEdges(context.Background(), deps, "own-graph", []crossGraphEdgeInput{{
		TargetGraph: crossTargetFamily, FromID: "ISSUE-1", ToID: crossTargetNode, Type: "tracked_by",
	}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no graph")
	assert.NotContains(t, err.Error(), "not found in",
		"with no graph of the family at all, the endpoint answer is the wrong one to report")
}

// TestResolveCrossGraphEdges_NoEdgesIssuesNoRead is the pass's own zero: a
// collect with nothing to resolve must not enumerate, so an ordinary collect
// pays nothing for this feature.
func TestResolveCrossGraphEdges_NoEdgesIssuesNoRead(t *testing.T) {
	deps, recorder, _ := crossGraphFixture(t, crossGraphPayload("", ""), true, true)
	require.NoError(t, resolveCrossGraphEdges(context.Background(), deps, "own-graph", nil))
	assert.Empty(t, recorder.execRequests, "an empty edge list issues no wire work at all")
}

// TestResolveCrossGraphEdges_RefusesAnEdgeWithNoEndpoint is the malformed-input
// arm: an edge naming a target graph but no endpoint id cannot be resolved and
// must error rather than reach the composer, whose LocateForeignNode returns
// not-found for an empty id and whose best-effort arm would then link an empty
// string.
func TestResolveCrossGraphEdges_RefusesAnEdgeWithNoEndpoint(t *testing.T) {
	deps, _, _ := crossGraphFixture(t, crossGraphPayload("", ""), true, true)
	for _, tc := range []struct {
		name string
		edge crossGraphEdgeInput
	}{
		{"no to_id", crossGraphEdgeInput{TargetGraph: crossTargetFamily, FromID: "ISSUE-1", Type: "tracked_by"}},
		{"no from_id", crossGraphEdgeInput{TargetGraph: crossTargetFamily, ToID: crossTargetNode, Type: "tracked_by"}},
		{"no type", crossGraphEdgeInput{TargetGraph: crossTargetFamily, FromID: "ISSUE-1", ToID: crossTargetNode}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := resolveCrossGraphEdges(context.Background(), deps, "own-graph", []crossGraphEdgeInput{tc.edge})
			require.Error(t, err)
			assert.True(t, strings.Contains(err.Error(), "empty") || strings.Contains(err.Error(), "no "),
				"the refusal names what was missing: %v", err)
		})
	}
}

// TestResolveCrossGraphEdges_RefusesWithoutAGraphCaller is the degraded-client
// arm. The post-collect linker WARNS and skips when the caller is nil, because
// its links are best-effort enrichment; a target-graph edge is not — the
// provider asserted an edge that cannot be written, so the collect fails.
//
// THE ASSERTION NAMES THIS PASS'S OWN MESSAGE, and that is not pedantry. A
// loose "contains graph client" passes on persistExecutor's refusal one line
// below, which fires for a nil caller too — so removing this guard entirely left
// the test green. The count of unresolvable edges is what only this refusal
// says, and it is the thing an operator needs: a degraded client is a state, a
// degraded client with three asserted cross-graph edges is a lost collect.
func TestResolveCrossGraphEdges_RefusesWithoutAGraphCaller(t *testing.T) {
	inner := newCustomDeps(t)
	err := resolveCrossGraphEdges(context.Background(), inner, "own-graph", []crossGraphEdgeInput{{
		TargetGraph: crossTargetFamily, FromID: "ISSUE-1", ToID: crossTargetNode, Type: "tracked_by",
	}, {
		TargetGraph: crossTargetFamily, FromID: "ISSUE-2", ToID: crossTargetNode, Type: "tracked_by",
	}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "2 cross-graph edge(s)",
		"the refusal counts what is being lost; persistExecutor's own nil-seam message would satisfy a looser assertion and did")
	assert.Contains(t, err.Error(), "own-graph", "and names the collect")
}

// crossGraphEdgeInput is a local alias for the carrier the collector contract
// hands the pass, so the rows above read as edges rather than as a package
// qualifier repeated twenty times.
type crossGraphEdgeInput = externalcollector.CrossGraphEdge
