// SPDX-License-Identifier: Apache-2.0

package crossgraph

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// scan_graphs_test.go — LinkRequest.ScanGraphs: resolving an endpoint against a
// graph family the composer's own fixed scan list does not carry.
//
// THE GAP THIS CLOSES, asserted here rather than described. ListForeignGraphs
// enumerates exactly code and practice. An endpoint living in a
// LOGS graph or in any registered custom family is therefore found by no probe,
// and on the linkage path the best-effort arm links it BY ITS RAW ID with no
// proxy at all — silently, since best-effort is not an error. The first test
// below pins that behavior as it stands; the second is the caller-supplied list
// that makes such an endpoint resolvable.

// jiraFixture is a fake whose only foreign node lives in a REGISTERED CUSTOM
// family. The builtin enumeration returns nothing for it, which is the point.
func jiraFixture() (*fakeCaller, *knowledgev1.Node) {
	issue := &knowledgev1.Node{Id: "BOARD-1", Type: "issue", SymbolName: "Login broken"}
	return &fakeCaller{
		nodesByGraph: map[string]map[string]*knowledgev1.Node{
			"jira": {issue.Id: issue},
		},
		// No entry for code/practice: the builtin scan finds nothing.
		graphNames: map[string][]string{"jira": {"board-a"}},
	}, issue
}

// mutationsOfKind returns the recorded plans of one mutation kind.
func mutationsOfKind(f *fakeCaller, kind knowledgev1.MutationPlan_MutationKind) []*knowledgev1.ExecuteRequest {
	var out []*knowledgev1.ExecuteRequest
	for _, p := range f.plans {
		if m := p.GetMutation(); m != nil && m.GetKind() == kind {
			out = append(out, p)
		}
	}
	return out
}

// TestResolveAndLink_WithoutScanGraphsACustomFamilyEndpointLinksRaw pins the
// behavior the caller-supplied list exists to change, so the improvement below
// is measured against a recorded before rather than an assumed one.
func TestResolveAndLink_WithoutScanGraphsACustomFamilyEndpointLinksRaw(t *testing.T) {
	f, issue := jiraFixture()

	handled, res, err := ResolveAndLink(context.Background(), f, f, LinkRequest{
		From: "ISSUE-LOCAL", To: issue.Id, Relationship: "relates_to",
		TargetGraph: "linkage", Stats: f.Stats,
	})
	require.NoError(t, err)
	require.True(t, handled)
	require.False(t, res.IsError)

	assert.Empty(t, mutationsOfKind(f, knowledgev1.MutationPlan_MUTATION_KIND_UPSERT),
		"the composer's fixed scan list carries no custom family, so no proxy is materialized")
	links := mutationsOfKind(f, knowledgev1.MutationPlan_MUTATION_KIND_LINK)
	require.Len(t, links, 1)
	assert.Equal(t, issue.Id, links[0].GetMutation().GetEdgeSpec().GetToId(),
		"the endpoint is linked by its RAW id — the best-effort arm, and not an error")
}

// TestResolveAndLink_ScanGraphsResolvesAgainstTheNamedFamily is the new
// capability: a caller that has already enumerated the family it cares about
// hands that list in, and the endpoint resolves to a proxy in the target graph.
//
// THE PROXY ID IS ASSERTED, not merely the presence of an upsert. A resolution
// that materialized a proxy under some other id would satisfy "a proxy was
// written" while producing an id no other reader can reconstruct.
func TestResolveAndLink_ScanGraphsResolvesAgainstTheNamedFamily(t *testing.T) {
	f, issue := jiraFixture()

	handled, res, err := ResolveAndLink(context.Background(), f, f, LinkRequest{
		From: "ISSUE-LOCAL", To: issue.Id, Relationship: "relates_to",
		TargetGraph: "linkage", Stats: f.Stats,
		ScanGraphs: []ForeignGraph{{GraphType: "jira", GraphName: "board-a"}},
	})
	require.NoError(t, err)
	require.True(t, handled)
	require.False(t, res.IsError)

	upserts := mutationsOfKind(f, knowledgev1.MutationPlan_MUTATION_KIND_UPSERT)
	require.Len(t, upserts, 1, "the located endpoint is materialized as exactly one proxy")
	assert.Equal(t, "linkage", upserts[0].GetTarget().GetGraph())
	assert.Equal(t, "proxy:custom/jira:board-a:BOARD-1",
		upserts[0].GetMutation().GetNodeBodies()[0].GetId(),
		"the proxy carries the generic arm's deterministic id")

	links := mutationsOfKind(f, knowledgev1.MutationPlan_MUTATION_KIND_LINK)
	require.Len(t, links, 1)
	assert.Equal(t, "proxy:custom/jira:board-a:BOARD-1", links[0].GetMutation().GetEdgeSpec().GetToId(),
		"the edge points at the proxy, never at the raw foreign id")
	assert.Equal(t, []string{"ISSUE-LOCAL"}, links[0].GetMutation().GetSelection().GetIds(),
		"the FROM is the collect's own node, which lives in no foreign graph and stays raw")
}

// TestResolveAndLink_ScanGraphsReplacesTheEnumerationRatherThanAddingToIt is the
// second half of the contract, and it is what the collect-side pass depends on:
// a caller that supplies the list has ALREADY enumerated, and the composer must
// not enumerate again. Without this the pass would pay four extra reads per
// collect and, worse, the composer's silent enumeration-failure arm would still
// be reachable on a path whose whole point is that an enumeration failure is an
// error.
func TestResolveAndLink_ScanGraphsReplacesTheEnumerationRatherThanAddingToIt(t *testing.T) {
	f, issue := jiraFixture()

	_, _, err := ResolveAndLink(context.Background(), f, f, LinkRequest{
		From: "ISSUE-LOCAL", To: issue.Id, Relationship: "relates_to",
		TargetGraph: "linkage", Stats: f.Stats,
		ScanGraphs: []ForeignGraph{{GraphType: "jira", GraphName: "board-a"}},
	})
	require.NoError(t, err)

	for _, p := range f.plans {
		assert.NotEqual(t, knowledgev1.ReturnMode_RETURN_MODE_GRAPH_NAMES, p.GetQuery().GetReturnMode(),
			"a supplied ScanGraphs list means the caller enumerated; the composer must issue no enumeration of its own")
	}

	// THE SAME-RUN CONTROL: the identical drive WITHOUT the list does enumerate,
	// so the zero above is a property of the list and not of this fixture.
	g, _ := jiraFixture()
	_, _, err = ResolveAndLink(context.Background(), g, g, LinkRequest{
		From: "ISSUE-LOCAL", To: issue.Id, Relationship: "relates_to",
		TargetGraph: "linkage", Stats: g.Stats,
	})
	require.NoError(t, err)
	enumerations := 0
	for _, p := range g.plans {
		if p.GetQuery().GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_GRAPH_NAMES {
			enumerations++
		}
	}
	assert.Equal(t, len(foreignScanGraphTypes), enumerations,
		"the unsupplied path enumerates once per family in the fixed scan list")
}
