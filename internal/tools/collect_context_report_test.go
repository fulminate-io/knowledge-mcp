// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/collector"
	"github.com/fulminate-io/knowledge-mcp/internal/externalcollector"
)

// collect_context_report_test.go — the per-declared-type match count, and the
// pair it exists to separate.
//
// EVERY ROW HERE DRIVES THE REAL fillCollectContext against the package's own
// fake graph caller. Nothing re-implements the fill.

// typedResourceCaller is a graph holding three nodes of ONE type, plus a second
// graph of the same family holding nothing at all. The two are the pair the
// report must separate: declaring a type the first graph does not hold produces
// exactly the block an empty graph produces.
func typedResourceCaller() *contextGraphCaller {
	nodes := make([]*knowledgev1.Node, 0, 3)
	for _, id := range []string{"r-1", "r-2", "r-3"} {
		nodes = append(nodes, &knowledgev1.Node{
			Id: id, Type: "cloud-resource", SymbolName: "svc-" + id,
			Metadata: map[string]string{"resource_type": "ec2:instance"},
		})
	}
	return &contextGraphCaller{
		names: map[string][]string{"aws": {"empty", "prod"}},
		graphs: map[string]map[string][]*knowledgev1.Node{"aws": {
			"prod": nodes,
		}},
	}
}

func fillWith(
	t *testing.T, caller *contextGraphCaller, families []string, decl externalcollector.ContextDeclaration,
) (*externalcollector.CollectContext, *contextFillReport) {
	t.Helper()
	deps := &customDeps{crud: registeredCRUD(families...), graphs: caller}
	block, report, err := fillCollectContext(context.Background(), deps, decl)
	require.NoError(t, err)
	return block, report
}

// TestTheBlockAloneCannotTellAWrongTypeFromAnEmptyGraph is row 3.1, the
// OBSERVED half, and it is the reason the report exists. It asserts the DEFECT
// rather than the fix: the two blocks are byte-identical.
func TestTheBlockAloneCannotTellAWrongTypeFromAnEmptyGraph(t *testing.T) {
	caller := typedResourceCaller()

	// A declared type the graph does not hold. `prod` really carries three nodes
	// — of another type — and `empty` carries none at all.
	block, _ := fillWith(t, caller, []string{"aws"}, externalcollector.ContextDeclaration{
		"aws": {NodeTypes: []string{"aws-resource"}, NodeFields: []string{externalcollector.ContextNodeFieldID}},
	})
	require.NotNil(t, block)
	body, err := json.Marshal(block)
	require.NoError(t, err)

	var graphs map[string][]map[string]any
	require.NoError(t, json.Unmarshal(body, &graphs))
	require.Len(t, graphs["aws"], 2, "both graph instances are present in the block")

	// THE PAIR: the graph holding three nodes of ANOTHER type and the graph
	// holding nothing render identically, key for key. Only the graph NAME
	// differs, and a name is not an answer about what the graph holds — which is
	// exactly why an operator reading the block cannot tell the two apart.
	populatedButUnmatched := graphs["aws"][1]
	genuinelyEmpty := graphs["aws"][0]
	require.Equal(t, "prod", populatedButUnmatched["graph_name"])
	require.Equal(t, "empty", genuinelyEmpty["graph_name"])
	delete(populatedButUnmatched, "graph_name")
	delete(genuinelyEmpty, "graph_name")
	assert.Equal(t, genuinelyEmpty, populatedButUnmatched,
		"a graph holding three nodes of an undeclared type and a graph holding nothing must be "+
			"identical in the block below the name — if they are not, this row's premise is stale")
	assert.Equal(t, map[string]any{"nodes": nil, "edges": nil}, populatedButUnmatched,
		"and what both carry is a null node list and a null edge list")

	// THE SAME-RUN CONTROL: the graph really does hold three selectable nodes,
	// so the identity above is a statement about the DECLARED TYPE rather than
	// about a fixture that holds nothing.
	control, _ := fillWith(t, caller, []string{"aws"}, externalcollector.ContextDeclaration{
		"aws": {NodeTypes: []string{"cloud-resource"}, NodeFields: []string{externalcollector.ContextNodeFieldID}},
	})
	require.NotNil(t, control)
	assert.Len(t, (*control)["aws"][1].Nodes, 3, "control: prod holds three cloud-resource nodes")
}

// TestTheReportSeparatesAWrongTypeFromAnEmptyGraph is row 3.3, the fix. The same
// two graphs, the same call, and the report tells them apart.
func TestTheReportSeparatesAWrongTypeFromAnEmptyGraph(t *testing.T) {
	caller := typedResourceCaller()

	_, wrong := fillWith(t, caller, []string{"aws"}, externalcollector.ContextDeclaration{
		"aws": {NodeTypes: []string{"aws-resource"}, NodeFields: []string{externalcollector.ContextNodeFieldID}},
	})
	_, right := fillWith(t, caller, []string{"aws"}, externalcollector.ContextDeclaration{
		"aws": {NodeTypes: []string{"cloud-resource"}, NodeFields: []string{externalcollector.ContextNodeFieldID}},
	})

	assert.Equal(t,
		"foreign context: aws/empty aws-resource 0; aws/prod aws-resource 0.",
		wrong.Render(),
		"a declared type nothing in either graph carries reports a zero for each")
	assert.Equal(t,
		"foreign context: aws/empty cloud-resource 0; aws/prod cloud-resource 3.",
		right.Render(),
		"and the emitted type reports the three nodes prod really holds")

	assert.NotEqual(t, wrong.Render(), right.Render(),
		"the two runs are indistinguishable in the block; the report is what separates them")
}

// TestTheReportRendersAZeroAsAZero is the property the whole report turns on: a
// row is never omitted because its count was zero.
func TestTheReportRendersAZeroAsAZero(t *testing.T) {
	_, report := fillWith(t, typedResourceCaller(), []string{"aws"},
		externalcollector.ContextDeclaration{"aws": {
			NodeTypes:  []string{"cloud-resource", "aws-resource"},
			NodeFields: []string{externalcollector.ContextNodeFieldID},
		}})
	rendered := report.Render()
	assert.Contains(t, rendered, "aws-resource 0",
		"the type that matched nothing must appear WITH ITS ZERO; omitting it reports exactly the "+
			"state this report exists to expose")
	assert.Contains(t, rendered, "cloud-resource 3", "and the one that matched reports its count")
}

// TestTheReportMatrix walks every cell of row 3.4.
func TestTheReportMatrix(t *testing.T) {
	idOnly := []string{externalcollector.ContextNodeFieldID}

	t.Run("family with several graph instances, one matching and one not", func(t *testing.T) {
		_, report := fillWith(t, typedResourceCaller(), []string{"aws"},
			externalcollector.ContextDeclaration{"aws": {NodeTypes: []string{"cloud-resource"}, NodeFields: idOnly}})
		assert.Equal(t, "foreign context: aws/empty cloud-resource 0; aws/prod cloud-resource 3.", report.Render())
	})

	t.Run("family with no graphs at all", func(t *testing.T) {
		caller := &contextGraphCaller{names: map[string][]string{"aws": {}}}
		_, report := fillWith(t, caller, []string{"aws"},
			externalcollector.ContextDeclaration{"aws": {NodeTypes: []string{"cloud-resource"}, NodeFields: idOnly}})
		assert.Equal(t, "foreign context: aws (no graphs).", report.Render(),
			"a supplyable family this daemon holds no graph of is reported as such, not omitted")
	})

	t.Run("declaration naming several node types", func(t *testing.T) {
		_, report := fillWith(t, typedResourceCaller(), []string{"aws"},
			externalcollector.ContextDeclaration{"aws": {
				NodeTypes: []string{"cloud-resource", "proxy"}, NodeFields: idOnly,
			}})
		assert.Equal(t,
			"foreign context: aws/empty cloud-resource 0, proxy 0; aws/prod cloud-resource 3, proxy 0.",
			report.Render(), "every declared type gets its own count in every graph")
	})

	t.Run("declaration naming NO node types is a third state, not a zero", func(t *testing.T) {
		// THE SETTLED gcp SHAPE. A family declared with nothing carries the graph
		// NAMES and no nodes by the documented semantics, which is a real
		// declaration; reporting it as "matched 0" would name a defect where there
		// is a deliberate choice.
		_, report := fillWith(t, typedResourceCaller(), []string{"aws"},
			externalcollector.ContextDeclaration{"aws": {}})
		rendered := report.Render()
		assert.Equal(t, "foreign context: aws/empty (names only); aws/prod (names only).", rendered)
		assert.NotContains(t, rendered, " 0", "a names-only family is never rendered as a zero match")
	})

	t.Run("several families at once, sorted", func(t *testing.T) {
		caller := typedResourceCaller()
		caller.names["code"] = []string{"api"}
		caller.graphs["code"] = map[string][]*knowledgev1.Node{
			"api": {codeFileNode("api:chart", "deploy/Chart.yaml", "name: api")},
		}
		_, report := fillWith(t, caller, []string{"aws"}, externalcollector.ContextDeclaration{
			"aws":  {NodeTypes: []string{"cloud-resource"}, NodeFields: idOnly},
			"code": {NodeTypes: []string{"file"}, NodeFields: idOnly},
		})
		assert.Equal(t,
			"foreign context: aws/empty cloud-resource 0; aws/prod cloud-resource 3; code/api file 1.",
			report.Render(), "families are reported in the fill's own sorted order")
	})
}

// TestTheReportNeverNarrowsTheBlock is row 3.5. The nodes and edges a module
// receives must be identical whether or not the report is rendered.
func TestTheReportNeverNarrowsTheBlock(t *testing.T) {
	decl := externalcollector.ContextDeclaration{"aws": {
		NodeTypes:    []string{"cloud-resource"},
		NodeFields:   []string{externalcollector.ContextNodeFieldID, externalcollector.ContextNodeFieldSymbolName},
		MetadataKeys: []string{"resource_type"},
	}}
	block, report := fillWith(t, typedResourceCaller(), []string{"aws"}, decl)

	before, err := json.Marshal(block)
	require.NoError(t, err)
	_ = report.Render()
	after, err := json.Marshal(block)
	require.NoError(t, err)
	assert.JSONEq(t, string(before), string(after), "rendering the report does not touch the block")

	// AND THE BLOCK IS WHOLE: three nodes, each carrying exactly the declared
	// fields. A report that had narrowed anything would show here.
	require.Len(t, (*block)["aws"][1].Nodes, 3)
	for _, n := range (*block)["aws"][1].Nodes {
		assert.NotEmpty(t, n.ID)
		assert.NotEmpty(t, n.SymbolName)
		assert.Equal(t, "ec2:instance", n.Metadata["resource_type"])
	}
}

// TestAnEmptyReportRendersNothing is row 3.6: a collect whose entry declares no
// context prints byte-identically to a tree without this report.
func TestAnEmptyReportRendersNothing(t *testing.T) {
	block, report := fillWith(t, typedResourceCaller(), nil, nil)
	assert.Nil(t, block, "an entry declaring nothing receives no block")
	assert.Nil(t, report, "and produces no report")
	assert.Empty(t, report.Render(), "a nil report renders the empty string")

	// AND THE SUFFIXING CONTRACT DEGRADES, which is what makes the collect text
	// unchanged rather than merely short.
	const text = "Collected aws prod — streamed to server. nodes 3, edges 1"
	assert.Equal(t, text, withComposition(text, report.Render()))

	// THE SAME-RUN KNOWN POSITIVE on that helper: a non-empty report IS appended,
	// so the equality above is a statement about the empty case rather than about
	// a suffix that never applies.
	_, populated := fillWith(t, typedResourceCaller(), []string{"aws"},
		externalcollector.ContextDeclaration{"aws": {
			NodeTypes: []string{"cloud-resource"}, NodeFields: []string{externalcollector.ContextNodeFieldID},
		}})
	assert.True(t, strings.HasPrefix(withComposition(text, populated.Render()), text+" foreign context: "))
}

// TestTheReportDoesNotTurnAReadErrorIntoAZero is the never-implicit read-outcome
// row. A drain that failed and a type that matched nothing are opposite answers,
// and a report that rendered the first as `0` would be the same silence in a new
// place.
func TestTheReportDoesNotTurnAReadErrorIntoAZero(t *testing.T) {
	caller := typedResourceCaller()
	caller.err = assertAnError{}
	deps := &customDeps{crud: registeredCRUD("aws"), graphs: caller}

	block, report, err := fillCollectContext(context.Background(), deps,
		externalcollector.ContextDeclaration{"aws": {
			NodeTypes: []string{"cloud-resource"}, NodeFields: []string{externalcollector.ContextNodeFieldID},
		}})
	require.Error(t, err, "a failed read fails the fill; it is never reported as a count")
	assert.Nil(t, block)
	assert.Nil(t, report)
	assert.Empty(t, report.Render())
}

// TestTheFillReportReachesTheCollectOutput is the ARRIVAL row, and it is the
// half the render rows above cannot reach. A report that is computed correctly
// and never leaves the fill is exactly as useful to a tester as no report at
// all — which is the shape of every defect this ticket fixes.
//
// It drives the REAL collectWork with a runner in the production shape, so what
// is asserted is the composition text a collect actually returns.
func TestTheFillReportReachesTheCollectOutput(t *testing.T) {
	deps := &detachFullDeps{rt: NewCollectRuntime(), gc: &fakeGraphCaller{}}

	const fill = "foreign context: aws/prod cloud-resource 0."
	reportingRunner := func(
		_ context.Context, _ collectArgs, _ collector.CollectOptions,
	) (collector.CollectComposition, string, error) {
		return collector.CollectComposition{}, fill, nil
	}

	registerDetachStub()
	detachStubStarted = make(chan struct{})
	detachStubRelease = make(chan struct{})
	close(detachStubRelease)

	composition, _, err := collectWork(context.Background(), deps,
		collectArgs{Type: detachFullPathType, ID: "fill-report-id"},
		collector.CollectOptions{Sink: noopSink{}}, "", false, reportingRunner)
	require.NoError(t, err)
	assert.Equal(t, "nodes 0, edges 0 "+fill, composition,
		"the collect's composition text must carry the fill report; a report the caller never sees "+
			"is the same silence this ticket exists to end")

	// THE CONTROL, same call path: a runner reporting NO fill returns the
	// composition byte-identically to a tree without this report.
	detachStubStarted = make(chan struct{})
	detachStubRelease = make(chan struct{})
	close(detachStubRelease)
	silent := func(
		_ context.Context, _ collectArgs, _ collector.CollectOptions,
	) (collector.CollectComposition, string, error) {
		return collector.CollectComposition{}, "", nil
	}
	unchanged, _, err := collectWork(context.Background(), deps,
		collectArgs{Type: detachFullPathType, ID: "no-fill-id"},
		collector.CollectOptions{Sink: noopSink{}}, "", false, silent)
	require.NoError(t, err)
	assert.Equal(t, "nodes 0, edges 0", unchanged)
}
