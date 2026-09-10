// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/externalcollector"
)

// collect_context_selector_test.go — the FAMILY-LEVEL SELECTOR: a declaration
// that asks for every node of a family, whatever its type.
//
// WHY IT EXISTS. A cloud provider emits one node type per resource kind — dozens
// of them — and before this a declaration could select them only by naming each
// exactly, which is a list that rots the day the provider adds a resource. A
// module that named a stale list received a silent zero.
//
// THE EMPTY NODE-TYPE LIST KEEPS ITS OWN MEANING, and the regression row below
// is what says so: empty is the graph names alone, the selector is every node,
// and they are two declarations rather than two readings of one.

// mixedTypeCaller is a registered family holding nodes of TWO distinct types
// with an edge between them.
//
// TWO TYPES IS THE POINT: a single-type graph cannot tell "every type" from "the
// one type that happens to be there", so a row asserting the selector on such a
// fixture would pass against an implementation that drained one arbitrary type.
func mixedTypeCaller() *contextGraphCaller {
	return &contextGraphCaller{
		names: map[string][]string{"aws": {"prod"}},
		graphs: map[string]map[string][]*knowledgev1.Node{"aws": {
			"prod": {
				{Id: "i-1", Type: "aws:ec2:instance", SymbolName: "api-server",
					Metadata: map[string]string{"region": "eu-west-2"}},
				{Id: "b-1", Type: "aws:s3:bucket", SymbolName: "artifacts",
					Metadata: map[string]string{"region": "eu-west-2"}},
			},
		}},
		edges: map[string]map[string][]knowledgev1.Edge{"aws": {
			"prod": {{FromId: "i-1", ToId: "b-1", Type: "READS_FROM"}},
		}},
	}
}

// selectorDeps wires the mixed-type fixture behind a declaring entry, returning
// the fixture beside the deps so a row can read which browses it answered.
func selectorDeps(t *testing.T, url string, decl externalcollector.ContextDeclaration) (*customDeps, *contextGraphCaller) {
	t.Helper()
	deps := newCustomDeps(t, declaringEntry(url, decl))
	deps.crud = registeredCRUD("aws")
	caller := mixedTypeCaller()
	deps.graphs = caller
	return deps, caller
}

// TestCustomCollect_AllNodeTypesDrainsEveryTypeAndItsEdges is the selector's
// central row, driven through the real fill against a graph seeded with two
// distinct types and an edge between them.
func TestCustomCollect_AllNodeTypesDrainsEveryTypeAndItsEdges(t *testing.T) {
	url, seen := argEchoProvider(t)
	decl := externalcollector.ContextDeclaration{
		"aws": {
			AllNodeTypes: true,
			NodeFields:   []string{"id", "type"},
			MetadataKeys: []string{"region"},
			EdgeFields:   []string{"from_id", "to_id"},
		},
	}
	deps, caller := selectorDeps(t, url, decl)

	handled, res := callCollect(deps, `{"type":"jira","id":"board"}`)
	require.True(t, handled)
	require.False(t, res.IsError, resultText(res))

	block, present := seen.contextBlock(t)
	require.True(t, present)
	require.Len(t, block["aws"], 1)
	graph := block["aws"][0]

	types := map[string]int{}
	for _, n := range graph.Nodes {
		types[n.Type]++
	}
	assert.Equal(t, map[string]int{"aws:ec2:instance": 1, "aws:s3:bucket": 1}, types,
		"the selector admits every node of the family, of MORE THAN ONE type — which is what a fixture with two types can show and a fixture with one cannot")
	assert.Equal(t, "eu-west-2", graph.Nodes[0].Metadata["region"],
		"the declared metadata keys ride the selected nodes, which is what the widened validator arms exist for")
	require.Len(t, graph.Edges, 1, "and the edge read is pivoted on the ids the selector carried")
	assert.Equal(t, "i-1", graph.Edges[0].FromID)
	assert.Equal(t, "b-1", graph.Edges[0].ToID)

	// The drain issued ONE typeless browse rather than one browse per type: a
	// per-type read cannot see a type nobody named.
	assert.Equal(t, []string{"aws/prod/*"}, caller.browsedReads(),
		"a family asking for every type is read with no type key at all")
}

// TestCustomCollect_TheTicket39ShapeIsTheSameRunControl is the CONTROL the row
// above needs, in the same run and through the same instrument: the shape a
// declaration is limited to WITHOUT the selector — the family key with an empty
// node-type list — returns the graph name, zero nodes and zero edges.
//
// Without it the row above could not tell "the selector works" from "this
// fixture returns everything to anyone who asks".
func TestCustomCollect_TheTicket39ShapeIsTheSameRunControl(t *testing.T) {
	url, seen := argEchoProvider(t)
	decl := externalcollector.ContextDeclaration{"aws": {NodeTypes: []string{}}}
	deps, caller := selectorDeps(t, url, decl)

	handled, res := callCollect(deps, `{"type":"jira","id":"board"}`)
	require.True(t, handled)
	require.False(t, res.IsError, resultText(res))

	block, present := seen.contextBlock(t)
	require.True(t, present)
	require.Len(t, block["aws"], 1)
	assert.Equal(t, "prod", block["aws"][0].GraphName, "the graph name is always carried")
	assert.Empty(t, block["aws"][0].Nodes, "zero nodes: this is the shape the selector replaces")
	assert.Empty(t, block["aws"][0].Edges, "and zero edges, because the edge read is pivoted on carried ids")
	assert.Empty(t, caller.browsedReads(), "and it costs no browse at all")
}

// TestContextDeclaration_TheSelectorAndAListTogetherAreRefused is the ambiguity
// refusal, asserted at LOAD time — the half that runs without a registry — and
// in BOTH key orders, because a JSON object's key order is not a promise.
func TestContextDeclaration_TheSelectorAndAListTogetherAreRefused(t *testing.T) {
	for _, raw := range []string{
		`{"aws":{"all_node_types":true,"node_types":["aws:ec2:instance"]}}`,
		`{"aws":{"node_types":["aws:ec2:instance"],"all_node_types":true}}`,
	} {
		decl := decodeContextDeclaration(t, raw)
		err := decl.Validate()
		require.Error(t, err, "a declaration saying both things must be refused: %s", raw)
		assert.Contains(t, err.Error(), "all_node_types")
		assert.Contains(t, err.Error(), "node_types")
		assert.Contains(t, err.Error(), "aws", "the refusal names the family")
	}
}

// TestContextDeclaration_TheSelectorSatisfiesTheThreeCoherenceArms is the
// validator widening, one row per arm. Each of the three refuses a declaration
// asking for node facts with no node to carry them; a family draining every type
// carries nodes, so each must ADMIT the selector while still refusing the
// declaration that selects nothing at all.
func TestContextDeclaration_TheSelectorSatisfiesTheThreeCoherenceArms(t *testing.T) {
	for _, tc := range []struct {
		name       string
		withFlag   string
		without    string
		wantErrHas string
	}{
		{
			"node fields",
			`{"aws":{"all_node_types":true,"node_fields":["id"]}}`,
			`{"aws":{"node_fields":["id"]}}`,
			"node fields or metadata keys",
		},
		{
			"metadata keys",
			`{"aws":{"all_node_types":true,"metadata_keys":["region"]}}`,
			`{"aws":{"metadata_keys":["region"]}}`,
			"node fields or metadata keys",
		},
		{
			"edge fields",
			`{"aws":{"all_node_types":true,"node_fields":["id"],"edge_fields":["from_id"]}}`,
			`{"aws":{"edge_fields":["from_id"]}}`,
			"edge_fields",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.NoError(t, decodeContextDeclaration(t, tc.withFlag).Validate(),
				"the selector carries nodes, so the fields it declares have somewhere to land")
			require.Error(t, decodeContextDeclaration(t, tc.without).Validate(),
				"and the arm still refuses a declaration that selects NOTHING — the widening must not have removed the check")
			assert.Contains(t, decodeContextDeclaration(t, tc.without).Validate().Error(), tc.wantErrHas)
		})
	}

	// The id-field arm keeps its condition UNCHANGED: edge fields without the id
	// node field is refused whether or not the selector is set, because the edge
	// read is pivoted on ids a node arrives without when the field was not
	// declared.
	err := decodeContextDeclaration(t, `{"aws":{"all_node_types":true,"edge_fields":["from_id"]}}`).Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"id"`)
}
