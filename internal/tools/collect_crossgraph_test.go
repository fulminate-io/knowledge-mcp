// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// collect_crossgraph_test.go — R2 and R3: a registered custom collect whose
// output carries an edge naming a target graph lands that edge in the LINKAGE
// graph, never in the collector's own graph, and refuses loudly before the
// shared resolver when the named graph, the endpoint or the enumeration is not
// there.
//
// THE INSTRUMENT IS THE RECORDER, NOT THE SINK ALONE. customDeps.GraphCaller()
// is nil, so a collect driven on a bare newCustomDeps observes no wire activity
// at all and would MANUFACTURE the zero this file rests on. Every drive here
// goes through crossGraphFixture, which wires the recording caller into both
// graph-caller accessors.

const (
	// crossTargetFamily is a REGISTERED CUSTOM family, deliberately not one of
	// the composer's four builtin scan families: an endpoint there is reachable
	// only through the pass's own enumeration.
	crossTargetFamily = "acme-tracker"
	crossTargetGraph  = "board-a"
	crossTargetNode   = "TRACK-9"

	// The SECOND family. A second registered custom family is the only way to
	// drive the transition where the pass must enumerate twice, and it has to be
	// a different family rather than a second graph of the first: the cache is
	// keyed by family, so two graphs of one family would still be one read and
	// would say nothing about the key.
	crossSecondFamily = "acme-wiki"
	crossSecondGraph  = "space-1"
	crossSecondNode   = "WIKI-2"
)

// crossGraphPayload is a provider result with one own-graph node, one own-graph
// edge, and one edge naming target. An empty target yields a payload with no
// cross-graph edge at all — the control shape.
func crossGraphPayload(target, toID string) any {
	edges := []any{
		map[string]any{"from_id": "ISSUE-1", "to_id": "ISSUE-2", "type": "blocks"},
	}
	if target != "" {
		edges = append(edges, map[string]any{
			"from_id": "ISSUE-1", "to_id": toID, "type": "tracked_by", "target_graph": target,
		})
	}
	return map[string]any{
		"nodes":         []any{map[string]any{"id": "ISSUE-1", "type": "issue"}},
		"edges":         edges,
		"walk_complete": true,
	}
}

// crossGraphFixture wires a registered custom collect whose recorder enumerates
// crossTargetFamily and resolves crossTargetNode inside it.
//
// seedTarget=false is the "named graph does not exist" arm: the family
// enumerates empty. seedNode=false is the "endpoint is not in it" arm: the
// family enumerates but holds no such node.
func crossGraphFixture(t *testing.T, payload any, seedTarget, seedNode bool) (ClientDeps, *fakeGraphCaller, *capturingSink) {
	t.Helper()
	registerShadowStub(t)
	mapGraphTypeForTest(t, shadowStubType, kgtypes.GraphPractice)

	body := `{"graphs":[]}`
	if seedTarget {
		body = `{"graphs":[{"graph_type":"` + crossTargetFamily + `","graph_name":"` + crossTargetGraph + `"}]}`
	}
	recorder := &fakeGraphCaller{
		listGraphsResult: &kgtools.ToolResult{
			Content: []kgtools.ContentBlock{{Type: "text", Text: body}},
		},
		// Name-aware seeding: the endpoint resolves ONLY in the named target
		// graph. The knowledge lookup resolveEndpoint tries first is keyed
		// ("knowledge","") and is deliberately absent, which is the real shape —
		// a foreign tracker id is not a knowledge node.
		queryResponsesByGraphName: map[graphKey]map[string]kgtools.ToolResult{
			{Type: crossTargetFamily, Name: crossTargetGraph}: {},
		},
	}
	if seedNode {
		recorder.queryResponsesByGraphName[graphKey{Type: crossTargetFamily, Name: crossTargetGraph}][crossTargetNode] =
			kgtools.ToolResult{Content: []kgtools.ContentBlock{{Type: "text", Text: `{"id":"` + crossTargetNode + `","type":"issue","symbol_name":"tracked"}`}}}
	}

	url := startCustomProvider(t, payload)
	inner := newCustomDeps(t, namedCustomDef(shadowStubType, url))
	return &customTailDeps{customDeps: inner, gc: recorder}, recorder, inner.sink
}

// sourceGraphPayload is the MIRROR of crossGraphPayload: the cross-graph edge's
// FROM is the foreign node and its TO is a node of the collect's own graph,
// which is the shape a Helm chart deploying a workload produces.
//
// IT TAKES NO PARAMETERS, unlike its sibling, and that asymmetry is real rather
// than an oversight: crossGraphPayload's empty-target argument is how the
// no-cross-graph-edge control shape is produced, while every source-graph drive
// here names the fixture's own family and node, which crossGraphFixture seeds.
func sourceGraphPayload() any {
	return map[string]any{
		"nodes": []any{map[string]any{"id": "ISSUE-1", "type": "issue"}},
		"edges": []any{
			map[string]any{"from_id": "ISSUE-1", "to_id": "ISSUE-2", "type": "blocks"},
			map[string]any{"from_id": crossTargetNode, "to_id": "ISSUE-1", "type": "tracked_by", "source_graph": crossTargetFamily},
		},
		"walk_complete": true,
	}
}

// TestCustomCollect_SourceGraphEdgeLandsInLinkageAndNotInItsOwnGraph is R2 for
// the mirror field, both halves in one drive, and it is the whole point of the
// field: the FOREIGN endpoint is the FROM, so the proxy is the FROM and the
// collect's own node is the raw TO — the exact reverse of the sibling above.
//
// ASSERTING ONLY THE LINKAGE WRITE WOULD PASS while the edge ALSO landed in the
// collector's own graph, which is the failure R2 names first, so the sink's own
// result is read in the same run.
func TestCustomCollect_SourceGraphEdgeLandsInLinkageAndNotInItsOwnGraph(t *testing.T) {
	deps, recorder, sink := crossGraphFixture(t, sourceGraphPayload(), true, true)

	handled, res := callCollect(deps, `{"type":"`+shadowStubType+`","id":"own-graph"}`)
	require.True(t, handled)
	require.False(t, res.IsError, resultText(res))

	links := linkageMutations(recorder)
	require.NotEmpty(t, links, "the source-graph edge must reach the linkage graph")

	var linked, upserted *knowledgev1.MutationPlan
	for _, r := range links {
		switch r.GetMutation().GetKind() {
		case knowledgev1.MutationPlan_MUTATION_KIND_LINK:
			linked = r.GetMutation()
		case knowledgev1.MutationPlan_MUTATION_KIND_UPSERT:
			upserted = r.GetMutation()
		}
	}
	require.NotNil(t, upserted, "the foreign endpoint is materialized as a proxy in linkage")
	require.NotNil(t, linked, "the edge itself is linked in linkage")

	// The expected proxy id is COMPOSED HERE from the fixture's own family, graph
	// and node, never echoed back from the producer.
	wantProxy := "proxy:custom/" + crossTargetFamily + ":" + crossTargetGraph + ":" + crossTargetNode
	assert.Equal(t, wantProxy, upserted.GetNodeBodies()[0].GetId(),
		"the proxy carries the generic arm's deterministic id, so a later reader can reconstruct it")
	assert.Equal(t, []string{wantProxy}, linked.GetSelection().GetIds(),
		"the FROM is the PROXY: a source-graph edge's foreign endpoint is its source, which is what makes this field different from target_graph")
	assert.Equal(t, "ISSUE-1", linked.GetEdgeSpec().GetToId(),
		"and the TO is the collect's own node id, raw")

	// THE OTHER HALF: the collector's own graph carries the in-graph edge and
	// ONLY that. Without the conversion's matching widening the edge would be
	// here as well, dangling off a foreign id.
	own := sink.last()
	require.NotNil(t, own, "the collect still ships its own graph")
	require.Len(t, own.Edges, 1, "the source-graph edge must NOT be written into the collector's own graph")
	assert.Equal(t, "ISSUE-2", own.Edges[0].ToID)
}

// twoFamilyPayload is a provider result carrying the own-graph edge plus TWO
// cross-graph edges naming DIFFERENT families. secondFamily is a parameter so
// the same shape drives both the resolvable case and the one whose second
// family does not exist.
func twoFamilyPayload(secondFamily string) any {
	return map[string]any{
		"nodes": []any{map[string]any{"id": "ISSUE-1", "type": "issue"}},
		"edges": []any{
			map[string]any{"from_id": "ISSUE-1", "to_id": "ISSUE-2", "type": "blocks"},
			map[string]any{"from_id": "ISSUE-1", "to_id": crossTargetNode, "type": "tracked_by", "target_graph": crossTargetFamily},
			map[string]any{"from_id": "ISSUE-1", "to_id": crossSecondNode, "type": "documented_by", "target_graph": secondFamily},
		},
		"walk_complete": true,
	}
}

// twoFamilyFixture wires a collect whose recorder enumerates TWO families and
// resolves one endpoint in each. seedSecond=false leaves the second family
// unenumerated, which is the absent-graph arm.
//
// THE ORDER IN THE PAYLOAD IS LOAD-BEARING for the failing case: the resolvable
// edge comes FIRST, so a pass that linked as it walked would have committed that
// edge before it ever reached the one it must refuse.
func twoFamilyFixture(t *testing.T, payload any, seedSecond bool) (ClientDeps, *fakeGraphCaller, *capturingSink) {
	t.Helper()
	registerShadowStub(t)
	mapGraphTypeForTest(t, shadowStubType, kgtypes.GraphPractice)

	graphs := `{"graph_type":"` + crossTargetFamily + `","graph_name":"` + crossTargetGraph + `"}`
	if seedSecond {
		graphs += `,{"graph_type":"` + crossSecondFamily + `","graph_name":"` + crossSecondGraph + `"}`
	}
	seedNode := func(id, symbol string) kgtools.ToolResult {
		return kgtools.ToolResult{Content: []kgtools.ContentBlock{
			{Type: "text", Text: `{"id":"` + id + `","type":"issue","symbol_name":"` + symbol + `"}`},
		}}
	}
	recorder := &fakeGraphCaller{
		listGraphsResult: &kgtools.ToolResult{
			Content: []kgtools.ContentBlock{{Type: "text", Text: `{"graphs":[` + graphs + `]}`}},
		},
		queryResponsesByGraphName: map[graphKey]map[string]kgtools.ToolResult{
			{Type: crossTargetFamily, Name: crossTargetGraph}: {crossTargetNode: seedNode(crossTargetNode, "tracked")},
			{Type: crossSecondFamily, Name: crossSecondGraph}: {crossSecondNode: seedNode(crossSecondNode, "documented")},
		},
	}

	url := startCustomProvider(t, payload)
	inner := newCustomDeps(t, namedCustomDef(shadowStubType, url))
	return &customTailDeps{customDeps: inner, gc: recorder}, recorder, inner.sink
}

// graphNameEnumerations counts the family graph-name reads the recorder saw. It
// is the observable the per-family cache is about: one read per DISTINCT family,
// never one per edge and never one overall.
func graphNameEnumerations(f *fakeGraphCaller) int {
	n := 0
	for _, r := range f.execRequests {
		if r.GetQuery().GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_GRAPH_NAMES {
			n++
		}
	}
	return n
}

// upsertedProxyIDs returns the ids of every proxy upserted into linkage, in
// order.
func upsertedProxyIDs(f *fakeGraphCaller) []string {
	var out []string
	for _, r := range linkageMutations(f) {
		if m := r.GetMutation(); m.GetKind() == knowledgev1.MutationPlan_MUTATION_KIND_UPSERT {
			for _, b := range m.GetNodeBodies() {
				out = append(out, b.GetId())
			}
		}
	}
	return out
}

// linkageMutations returns the recorded mutation plans whose Target graph is
// linkage — the observable R2 rests on, and the same one the compiled-in
// linker's edges are visible through.
func linkageMutations(f *fakeGraphCaller) []*knowledgev1.ExecuteRequest {
	var out []*knowledgev1.ExecuteRequest
	for _, r := range f.execRequests {
		if r.GetMutation() != nil && r.GetTarget().GetGraph() == "linkage" {
			out = append(out, r)
		}
	}
	return out
}

// TestCustomCollect_TargetGraphEdgeLandsInLinkageAndNotInItsOwnGraph is R2, both
// halves in one drive.
//
// ASSERTING ONLY THE LINKAGE WRITE WOULD PASS while the edge ALSO landed in the
// collector's own graph, which is the failure R2 names first. So the sink's own
// result is read in the same run.
func TestCustomCollect_TargetGraphEdgeLandsInLinkageAndNotInItsOwnGraph(t *testing.T) {
	deps, recorder, sink := crossGraphFixture(t, crossGraphPayload(crossTargetFamily, crossTargetNode), true, true)

	handled, res := callCollect(deps, `{"type":"`+shadowStubType+`","id":"own-graph"}`)
	require.True(t, handled)
	require.False(t, res.IsError, resultText(res))

	links := linkageMutations(recorder)
	require.NotEmpty(t, links, "the target-graph edge must reach the linkage graph")

	var linked, upserted *knowledgev1.MutationPlan
	for _, r := range links {
		switch r.GetMutation().GetKind() {
		case knowledgev1.MutationPlan_MUTATION_KIND_LINK:
			linked = r.GetMutation()
		case knowledgev1.MutationPlan_MUTATION_KIND_UPSERT:
			upserted = r.GetMutation()
		}
	}
	require.NotNil(t, upserted, "the foreign endpoint is materialized as a proxy in linkage")
	require.NotNil(t, linked, "the edge itself is linked in linkage")
	assert.Equal(t, "proxy:custom/"+crossTargetFamily+":"+crossTargetGraph+":"+crossTargetNode,
		upserted.GetNodeBodies()[0].GetId(),
		"the proxy carries the generic arm's deterministic id, so a later reader can reconstruct it")
	assert.Equal(t, upserted.GetNodeBodies()[0].GetId(), linked.GetEdgeSpec().GetToId(),
		"the edge points at the proxy, never at the raw foreign id")
	assert.Equal(t, []string{"ISSUE-1"}, linked.GetSelection().GetIds(),
		"the FROM is the collect's own node id")

	// THE OTHER HALF: the collector's own graph carries the in-graph edge and
	// ONLY that.
	own := sink.last()
	require.NotNil(t, own, "the collect still ships its own graph")
	assert.Equal(t, kgtypes.GraphType(shadowStubType), own.GraphType)
	require.Len(t, own.Edges, 1, "the target-graph edge must NOT be written into the collector's own graph")
	assert.Equal(t, "ISSUE-2", own.Edges[0].ToID)
}

// TestCustomCollect_NoTargetGraphEdgeReachesNoLinkage is the same-run control
// for the assertion above and R2's zero-edge transition: the identical fixture
// with a payload naming no target graph writes nothing to linkage, so a
// non-empty linkage above is caused by the edge rather than by the collect.
func TestCustomCollect_NoTargetGraphEdgeReachesNoLinkage(t *testing.T) {
	deps, recorder, sink := crossGraphFixture(t, crossGraphPayload("", ""), true, true)

	handled, res := callCollect(deps, `{"type":"`+shadowStubType+`","id":"own-graph"}`)
	require.True(t, handled)
	require.False(t, res.IsError, resultText(res))

	assert.Empty(t, linkageMutations(recorder),
		"a collect with no target-graph edge must touch the linkage graph not at all")
	own := sink.last()
	require.NotNil(t, own)
	require.Len(t, own.Edges, 1, "its own edge is untouched")
}

// TestCustomCollect_EveryEdgeCrossGraphStillShipsAValidOwnGraph is the boundary
// transition: when every edge names a target graph the collect's own result
// carries zero edges and must still be a WRITE, not a refusal.
func TestCustomCollect_EveryEdgeCrossGraphStillShipsAValidOwnGraph(t *testing.T) {
	payload := map[string]any{
		"nodes": []any{map[string]any{"id": "ISSUE-1", "type": "issue"}},
		"edges": []any{map[string]any{
			"from_id": "ISSUE-1", "to_id": crossTargetNode, "type": "tracked_by", "target_graph": crossTargetFamily,
		}},
		"walk_complete": true,
	}
	deps, recorder, sink := crossGraphFixture(t, payload, true, true)

	handled, res := callCollect(deps, `{"type":"`+shadowStubType+`","id":"own-graph"}`)
	require.True(t, handled)
	require.False(t, res.IsError, resultText(res))

	assert.NotEmpty(t, linkageMutations(recorder))
	own := sink.last()
	require.NotNil(t, own, "an all-cross-graph result is still a collect that wrote its own graph")
	assert.Empty(t, own.Edges)
	require.Len(t, own.Nodes, 1)
}

// TestCustomCollect_SeveralTargetGraphsEnumerateOncePerNamedGraph pins the
// per-named-graph batching the pass's cost rests on: two edges naming the SAME
// family pay one enumeration, and a second family pays one more.
func TestCustomCollect_SeveralTargetGraphsEnumerateOncePerNamedGraph(t *testing.T) {
	payload := map[string]any{
		"nodes": []any{map[string]any{"id": "ISSUE-1", "type": "issue"}},
		"edges": []any{
			map[string]any{"from_id": "ISSUE-1", "to_id": crossTargetNode, "type": "tracked_by", "target_graph": crossTargetFamily},
			map[string]any{"from_id": "ISSUE-1", "to_id": crossTargetNode, "type": "mentions", "target_graph": crossTargetFamily},
		},
		"walk_complete": true,
	}
	deps, recorder, _ := crossGraphFixture(t, payload, true, true)

	handled, res := callCollect(deps, `{"type":"`+shadowStubType+`","id":"own-graph"}`)
	require.True(t, handled)
	require.False(t, res.IsError, resultText(res))

	enumerations := 0
	for _, r := range recorder.execRequests {
		if r.GetQuery().GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_GRAPH_NAMES {
			enumerations++
		}
	}
	assert.Equal(t, 1, enumerations,
		"two edges naming one family enumerate that family once, not once per edge")

	links := 0
	for _, r := range linkageMutations(recorder) {
		if r.GetMutation().GetKind() == knowledgev1.MutationPlan_MUTATION_KIND_LINK {
			links++
		}
	}
	assert.Equal(t, 2, links, "both edges are linked")
}

// TestCustomCollect_TwoTargetGraphFamiliesEnumerateOncePerFamily is the fourth
// collect-level transition: SEVERAL target-graph edges naming DIFFERENT
// families. The sibling above drives two edges of ONE family, which pins "not
// once per edge" and says nothing about the key.
//
// WHAT THE COUNT DISCRIMINATES, and why two is the only interesting number. One
// read would mean the cache is keyed by something constant, so the second family
// silently reuses the first family's graph list and its endpoint is looked for
// in the wrong graphs — a valid collect turned into a refusal. Three would mean
// the cache is not consulted at all and every edge pays a read. Two is the only
// answer that is both correct and caused by the key.
func TestCustomCollect_TwoTargetGraphFamiliesEnumerateOncePerFamily(t *testing.T) {
	deps, recorder, sink := twoFamilyFixture(t, twoFamilyPayload(crossSecondFamily), true)

	handled, res := callCollect(deps, `{"type":"`+shadowStubType+`","id":"own-graph"}`)
	require.True(t, handled)
	require.False(t, res.IsError, resultText(res))

	assert.Equal(t, 2, graphNameEnumerations(recorder),
		"two DISTINCT families enumerate twice: once each, never once per edge and never once overall")

	assert.ElementsMatch(t, []string{
		"proxy:custom/" + crossTargetFamily + ":" + crossTargetGraph + ":" + crossTargetNode,
		"proxy:custom/" + crossSecondFamily + ":" + crossSecondGraph + ":" + crossSecondNode,
	}, upsertedProxyIDs(recorder),
		"BOTH endpoints resolve, each in ITS OWN family's graph; a shared graph list would find the second nowhere")

	own := sink.last()
	require.NotNil(t, own)
	require.Len(t, own.Edges, 1, "only the in-graph edge reaches the collect's own graph")
	assert.Equal(t, "ISSUE-2", own.Edges[0].ToID)
}

// TestCustomCollect_AFailingEdgeLeavesNoPartialLinkageWork is the same
// transition's failing arm, and it is a REQUIREMENT rather than a preference:
// the refusals are specified to sit ahead of the resolver, and a collect that
// fails must leave the graph as it found it.
//
// THE SHAPE THAT MAKES IT BITE: a resolvable cross-graph edge FIRST, then one
// naming a family that does not exist. A pass that validated and linked in one
// walk would already have committed the first edge's proxy and link when it
// reached the second — and since the collect then fails, its own graph is never
// written, so linkage would hold an edge FROM a node id that exists in no graph.
// The sibling refusal test drives single-edge payloads, so its "no partial
// linkage work is done" message was broader than its own drive could show; this
// is the drive that shows it.
func TestCustomCollect_AFailingEdgeLeavesNoPartialLinkageWork(t *testing.T) {
	deps, recorder, sink := twoFamilyFixture(t, twoFamilyPayload("no-such-family"), false)

	handled, res := callCollect(deps, `{"type":"`+shadowStubType+`","id":"own-graph"}`)
	require.True(t, handled)
	require.True(t, res.IsError, "the collect must fail: %s", resultText(res))
	assert.Contains(t, resultText(res), "no-such-family", "the refusal names the family")
	assert.Contains(t, resultText(res), crossSecondNode, "and the edge")

	assert.Nil(t, sink.last(), "a failed collect ships no own graph")
	assert.Empty(t, linkageMutations(recorder),
		"and writes NOTHING to linkage: the earlier edge was resolvable, so a pass that linked as it walked would have left a proxy and an edge behind, dangling off a node id the failed collect never wrote")

	// THE SAME-RUN CONTROL, so the zero above is a property of the refusal and
	// not of a fixture that could never have written anything: the identical
	// payload with the second family present succeeds and writes both proxies.
	okDeps, okRecorder, okSink := twoFamilyFixture(t, twoFamilyPayload(crossSecondFamily), true)
	okHandled, okRes := callCollect(okDeps, `{"type":"`+shadowStubType+`","id":"own-graph"}`)
	require.True(t, okHandled)
	require.False(t, okRes.IsError, resultText(okRes))
	assert.Len(t, upsertedProxyIDs(okRecorder), 2, "the control writes the two proxies the failing drive must not leave behind")
	assert.NotNil(t, okSink.last())
}
