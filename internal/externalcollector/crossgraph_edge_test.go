// SPDX-License-Identifier: Apache-2.0

package externalcollector

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// crossgraph_edge_test.go — the contract's TARGET-GRAPH EDGE: the field on the
// envelope's Edge, its declaration in the published output schema, and the
// partition the conversion performs on it.
//
// THE PARTITION IS THE POINT. An edge naming a target graph is NOT an edge in
// the collector's own graph, so it must never reach the CollectResult the sink
// ships; it leaves the package through CrossGraphEdges instead, for the
// collector-side resolution pass to link into the linkage graph.

// targetGraphEdgePayload is a conforming provider result whose single edge names
// a target graph. It is written as raw JSON on purpose: the assertion is about
// what the CONTRACT admits over the wire, which a Go literal cannot exercise —
// the strict decode and the schema validation both read bytes.
const targetGraphEdgePayload = `{
	"walk_complete": true,
	"nodes": [{"id": "ISSUE-1", "type": "issue"}],
	"edges": [
		{"from_id": "ISSUE-1", "to_id": "arn:aws:iam::1234:role/r", "type": "assumes", "target_graph": "acme-aws"}
	]
}`

// sourceGraphEdgePayload is the MIRROR of the payload above: a conforming
// provider result whose single edge names a SOURCE graph, so its FROM is the
// foreign endpoint and its TO is a node of the collect's own graph. It is the
// shape the Kubernetes collector's Helm relationship produces.
const sourceGraphEdgePayload = `{
	"walk_complete": true,
	"nodes": [{"id": "prod/Deployment/api", "type": "workload"}],
	"edges": [
		{"from_id": "charts/api/Chart.yaml", "to_id": "prod/Deployment/api", "type": "DEPLOYS", "source_graph": "code"}
	]
}`

// TestDecodeResult_AdmitsAnEdgeNamingATargetGraph is R1's central admission:
// BOTH gates admit the field. The schema gate (ValidateResultPayload) asks
// whether the payload is a conforming collector result; the strict decode
// (DisallowUnknownFields) asks whether every key is one the envelope names. A
// field added to only one of the two is admitted by that gate and refused by the
// other, so both are driven here rather than the decode alone.
func TestDecodeResult_AdmitsAnEdgeNamingATargetGraph(t *testing.T) {
	require.NoError(t, ValidateResultPayload("collect_graph", []byte(targetGraphEdgePayload)),
		"the schema gate must admit an edge naming a target graph")

	var structured any
	require.NoError(t, json.Unmarshal([]byte(targetGraphEdgePayload), &structured))

	r, err := DecodeResult("collect_graph", structured)
	require.NoError(t, err, "the strict decode must admit an edge naming a target graph")
	require.Len(t, r.Edges, 1)
}

// TestDecodeResult_CarriesTheTargetGraphValue is the second half of the
// admission: admitted is not the same as CARRIED. A field the decoder tolerates
// but drops on the floor would pass the test above while making the whole
// carrier inert, which is the failure this assertion exists to catch.
func TestDecodeResult_CarriesTheTargetGraphValue(t *testing.T) {
	var structured any
	require.NoError(t, json.Unmarshal([]byte(targetGraphEdgePayload), &structured))
	r, err := DecodeResult("collect_graph", structured)
	require.NoError(t, err)
	require.Len(t, r.Edges, 1)
	assert.Equal(t, "acme-aws", r.Edges[0].TargetGraph)
}

// TestDecodeResult_AdmitsAnEdgeNamingASourceGraph is the same admission for the
// mirror field, and BOTH gates are driven for the same reason: the schema gate
// and the strict decode are two independent admissions, and a field added to one
// of them is admitted by that one and refused by the other. Before this change
// the strict decode refused the key by name, which is why the framework field,
// both schema copies and this envelope had to land in one change.
func TestDecodeResult_AdmitsAnEdgeNamingASourceGraph(t *testing.T) {
	require.NoError(t, ValidateResultPayload("collect_graph", []byte(sourceGraphEdgePayload)),
		"the schema gate must admit an edge naming a source graph")

	var structured any
	require.NoError(t, json.Unmarshal([]byte(sourceGraphEdgePayload), &structured))

	r, err := DecodeResult("collect_graph", structured)
	require.NoError(t, err, "the strict decode must admit an edge naming a source graph")
	require.Len(t, r.Edges, 1)
}

// TestDecodeResult_CarriesTheSourceGraphValue is the second half: admitted is
// not carried. A decoder that tolerated the key and dropped it would leave the
// whole carrier inert while the admission test above stayed green.
func TestDecodeResult_CarriesTheSourceGraphValue(t *testing.T) {
	var structured any
	require.NoError(t, json.Unmarshal([]byte(sourceGraphEdgePayload), &structured))
	r, err := DecodeResult("collect_graph", structured)
	require.NoError(t, err)
	require.Len(t, r.Edges, 1)
	assert.Equal(t, "code", r.Edges[0].SourceGraph)
	assert.Empty(t, r.Edges[0].TargetGraph,
		"and the mirror field stays empty: an edge naming both is refused by the collect pass")
}

// TestOutputContract_DeclaresTheSourceGraphProperty is the published schema's
// half, read off the EMBEDDED contract rather than the file, because the embed
// is what a provider is validated against.
func TestOutputContract_DeclaresTheSourceGraphProperty(t *testing.T) {
	var schema struct {
		Properties struct {
			Edges struct {
				Items struct {
					Required   []string                  `json:"required"`
					Properties map[string]map[string]any `json:"properties"`
				} `json:"items"`
			} `json:"edges"`
		} `json:"properties"`
	}
	require.NoError(t, json.Unmarshal(OutputContractJSON(), &schema))

	prop, declared := schema.Properties.Edges.Items.Properties["source_graph"]
	require.True(t, declared, "the published output schema must declare the source_graph edge property")
	assert.Equal(t, "string", prop["type"])
	assert.NotContains(t, schema.Properties.Edges.Items.Required, "source_graph",
		"source_graph is OPTIONAL: an edge without it is an in-graph edge or a target-graph edge, and both stay valid")
}

// TestOutputContract_DeclaresTheTargetGraphProperty asserts the PUBLISHED
// schema declares the property, read off the embedded contract rather than a
// re-read of the file: the embed is what a provider is validated against and
// what OutputContractJSON advertises to a collector author.
//
// IT IS NOT REQUIRED, and that is the assertion's second half: an edge without
// the field is an in-graph edge and stays valid.
func TestOutputContract_DeclaresTheTargetGraphProperty(t *testing.T) {
	var schema struct {
		Properties struct {
			Edges struct {
				Items struct {
					Required   []string                  `json:"required"`
					Properties map[string]map[string]any `json:"properties"`
				} `json:"items"`
			} `json:"edges"`
		} `json:"properties"`
	}
	require.NoError(t, json.Unmarshal(OutputContractJSON(), &schema))

	prop, declared := schema.Properties.Edges.Items.Properties["target_graph"]
	require.True(t, declared, "the published output schema must declare the target_graph edge property")
	assert.Equal(t, "string", prop["type"])
	assert.NotContains(t, schema.Properties.Edges.Items.Required, "target_graph",
		"target_graph is OPTIONAL: an edge without it is an in-graph edge and stays valid")
}

// TestToCollectResult_PartitionsTheTargetGraphEdgesOut is R2's structural half,
// asserted at the conversion rather than at the collect: the own-graph payload
// the sink ships carries ONLY the edges that named no target graph, and the ones
// that did leave through CrossGraphEdges instead.
//
// THE TWO HALVES ARE ASSERTED TOGETHER because either alone passes while the
// feature is broken: converting everything satisfies "the cross edges are
// returned", and dropping the cross edges on the floor satisfies "they are not
// in the CollectResult". The count assertion below is what pins that every
// envelope edge is in exactly one of the two.
func TestToCollectResult_PartitionsTheTargetGraphEdgesOut(t *testing.T) {
	r := Result{
		Nodes: []Node{{ID: "ISSUE-1", Type: "issue"}},
		Edges: []Edge{
			{FromID: "ISSUE-1", ToID: "ISSUE-2", Type: "blocks"},
			{FromID: "ISSUE-1", ToID: "role/r", Type: "assumes", TargetGraph: "acme-aws", Weight: 2, Confidence: 0.5, Method: "m", Evidence: "e"},
			{FromID: "ISSUE-1", ToID: "q-1:tpl", Type: "emitted_by", TargetGraph: "logs"},
		},
		WalkComplete: true,
	}

	cr, err := r.ToCollectResult("jira", "board")
	require.NoError(t, err)
	require.Len(t, cr.Edges, 1, "only the in-graph edge may reach the collect's own graph")
	assert.Equal(t, "ISSUE-2", cr.Edges[0].ToID)

	cross := r.CrossGraphEdges()
	require.Len(t, cross, 2, "every edge naming a target graph leaves through CrossGraphEdges")
	assert.Len(t, cr.Edges, len(r.Edges)-len(cross),
		"the two halves partition the envelope: nothing the provider emitted is dropped and nothing is counted twice")

	assert.Equal(t, CrossGraphEdge{
		TargetGraph: "acme-aws", FromID: "ISSUE-1", ToID: "role/r", Type: "assumes",
		Weight: 2, Confidence: 0.5, Method: "m", Evidence: "e",
	}, cross[0], "every wire-settable edge field rides across the partition")
	assert.Equal(t, "logs", cross[1].TargetGraph)
}

// TestToCollectResult_PartitionsTheSourceGraphEdgesOut is the same partition for
// the mirror field, and it is the row that catches the seam that fails SILENTLY.
//
// THE TWO CONDITIONS ARE EXACT COMPLEMENTS: CrossGraphEdges lifts an edge that
// names a family, ToCollectResult converts the ones that name none. Widen the
// lift for source_graph and forget the conversion and the edge is in BOTH —
// resolved into linkage AND written into the collector's own graph as a dangling
// edge to a chart id nothing there resolves. Nothing else in the tree fails on
// that, which is why the count assertion below is written as a partition rather
// than as two independent presence checks.
func TestToCollectResult_PartitionsTheSourceGraphEdgesOut(t *testing.T) {
	r := Result{
		Nodes: []Node{{ID: "prod/Deployment/api", Type: "workload"}},
		Edges: []Edge{
			{FromID: "prod/Deployment/api", ToID: "prod/Service/api", Type: "exposes"},
			{FromID: "charts/api/Chart.yaml", ToID: "prod/Deployment/api", Type: "DEPLOYS", SourceGraph: "code", Weight: 3, Confidence: 0.85, Method: "tier1-helm", Evidence: "label helm.sh/chart=api-1.2.3"},
		},
		WalkComplete: true,
	}

	cr, err := r.ToCollectResult("k8s", "prod")
	require.NoError(t, err)
	require.Len(t, cr.Edges, 1, "only the in-graph edge may reach the collect's own graph")
	assert.Equal(t, "prod/Service/api", cr.Edges[0].ToID)

	cross := r.CrossGraphEdges()
	require.Len(t, cross, 1, "the edge naming a source graph leaves through CrossGraphEdges")
	assert.Len(t, cr.Edges, len(r.Edges)-len(cross),
		"the two halves partition the envelope: an edge in BOTH would be resolved into linkage AND written into the collect's own graph, dangling off a foreign id")

	assert.Equal(t, CrossGraphEdge{
		SourceGraph: "code", FromID: "charts/api/Chart.yaml", ToID: "prod/Deployment/api", Type: "DEPLOYS",
		Weight: 3, Confidence: 0.85, Method: "tier1-helm", Evidence: "label helm.sh/chart=api-1.2.3",
	}, cross[0], "every wire-settable edge field rides across the partition, and TargetGraph stays empty")
}

// TestSourceGraph_PresentAndEmptyIsTheInGraphCase is the mirror's third input
// class: a provider that always writes the key and leaves it blank. It must be
// the IN-GRAPH case in both directions — inbound, because a blank family would
// otherwise ask the pass to enumerate the family ""; outbound, because an edge
// that names no family must marshal exactly as it did before this field existed.
func TestSourceGraph_PresentAndEmptyIsTheInGraphCase(t *testing.T) {
	const emptyValued = `{
		"walk_complete": true,
		"nodes": [{"id": "ISSUE-1", "type": "issue"}],
		"edges": [{"from_id": "ISSUE-1", "to_id": "ISSUE-2", "type": "blocks", "source_graph": ""}]
	}`

	// INBOUND: both gates admit it, and it converts as an ordinary edge.
	require.NoError(t, ValidateResultPayload("collect_graph", []byte(emptyValued)),
		"a present-but-blank source_graph is a conforming result")
	var structured any
	require.NoError(t, json.Unmarshal([]byte(emptyValued), &structured))
	r, err := DecodeResult("collect_graph", structured)
	require.NoError(t, err, "the strict decode admits the key whatever its value")
	require.Len(t, r.Edges, 1)
	assert.Empty(t, r.Edges[0].SourceGraph)

	cr, cerr := r.ToCollectResult("jira", "board")
	require.NoError(t, cerr)
	require.Len(t, cr.Edges, 1,
		"an empty value is the IN-GRAPH case: the edge belongs to the collect's own graph")
	assert.Empty(t, r.CrossGraphEdges(),
		"and it reaches no cross-graph resolution, which would otherwise be asked to enumerate the family \"\"")

	// OUTBOUND: unset and explicitly-empty marshal to the SAME bytes, and neither
	// carries the key. This is what omitempty buys and the only assertion here
	// that observes it.
	unset, uerr := json.Marshal(Edge{FromID: "A", ToID: "B", Type: "blocks"})
	require.NoError(t, uerr)
	blank, berr := json.Marshal(Edge{FromID: "A", ToID: "B", Type: "blocks", SourceGraph: ""})
	require.NoError(t, berr)
	assert.JSONEq(t, string(unset), string(blank),
		"absent and present-and-empty are indistinguishable on the way out")
	assert.NotContains(t, string(unset), "source_graph",
		"an edge that names no source graph marshals exactly as it did before this field existed")
}

// TestCrossGraphEdges_EmptyWhenNoEdgeNamesAGraph is the partition's negative
// arm: an envelope of ordinary edges yields no cross-graph work at all, so the
// collect path's pass is reached with nothing to do rather than with a
// zero-valued edge.
func TestCrossGraphEdges_EmptyWhenNoEdgeNamesAGraph(t *testing.T) {
	r := Result{Edges: []Edge{{FromID: "a", ToID: "b", Type: "blocks"}}}
	assert.Empty(t, r.CrossGraphEdges())

	var nilResult *Result
	assert.Empty(t, nilResult.CrossGraphEdges(), "a nil Result carries no cross-graph edges")
}

// TestEdgeStructCarriesEveryEdgePropertyTheContractDeclares is the STRUCTURAL
// agreement between the envelope's Edge and the published schema, on this side
// of the contract. Its twin lives in cmd/collectors/framework, over that
// module's own copy of the same struct: the contract has two Go arms and a gate
// on one of them is how the other silently loses a field.
//
// The existing TestContractSchemaAndEnvelopeAgree compares TOP-LEVEL fields
// only, so an edge property declared in the schema and absent from Edge — which
// would be validated in, strictly refused at the decode, and unusable — passes
// every gate that existed before this one.
func TestEdgeStructCarriesEveryEdgePropertyTheContractDeclares(t *testing.T) {
	var schema struct {
		Properties struct {
			Edges struct {
				Items struct {
					Properties map[string]json.RawMessage `json:"properties"`
				} `json:"items"`
			} `json:"edges"`
		} `json:"properties"`
	}
	require.NoError(t, json.Unmarshal(OutputContractJSON(), &schema))
	declared := schema.Properties.Edges.Items.Properties
	require.NotEmpty(t, declared, "the contract declares no edge properties; this gate would pass vacuously")

	tags := map[string]bool{}
	rt := reflect.TypeFor[Edge]()
	for field := range rt.Fields() {
		name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if name != "" && name != "-" {
			tags[name] = true
		}
	}
	for property := range declared {
		assert.True(t, tags[property],
			"the contract declares the edge property %q and Edge has no field with that json tag, so a provider setting it is refused by the strict decode", property)
	}
	// The reverse direction is deliberately NOT asserted: the schema is a floor
	// rather than an enumeration, and weight, confidence, method and evidence are
	// legitimately undeclared in it.
}

// TestDecodeResult_AnEdgeWithoutTheFieldIsUnchanged is the absent-field control
// for the two assertions above, in the same instrument. Without it, a schema
// that made the property required would still pass the admission test while
// breaking every collector already in the field.
func TestDecodeResult_AnEdgeWithoutTheFieldIsUnchanged(t *testing.T) {
	const plain = `{
		"walk_complete": true,
		"nodes": [{"id": "ISSUE-1", "type": "issue"}],
		"edges": [{"from_id": "ISSUE-1", "to_id": "ISSUE-2", "type": "blocks"}]
	}`
	require.NoError(t, ValidateResultPayload("collect_graph", []byte(plain)))

	var structured any
	require.NoError(t, json.Unmarshal([]byte(plain), &structured))
	r, err := DecodeResult("collect_graph", structured)
	require.NoError(t, err)
	require.Len(t, r.Edges, 1)
	assert.Equal(t, "ISSUE-2", r.Edges[0].ToID)
}

// TestTargetGraph_PresentAndEmptyIsTheInGraphCase is the THIRD input class, and
// it is the one that decides whether omitempty is doing the job it was chosen
// for. The absent key and the present-with-a-value cases are covered above; a
// provider that writes the key and leaves it blank is neither, and it is a shape
// real emitters produce all the time — a template that always emits the field,
// a struct marshaled without omitempty on the far side, a config value that
// resolved to nothing.
//
// ABSENT AND EMPTY MUST BE INDISTINGUISHABLE DOWNSTREAM, in BOTH directions.
// Inbound, an empty value is an in-graph edge and reaches no cross-graph
// resolution: were it treated as cross-graph it would name the family "", which
// the pass refuses, so a blank key would fail a collect that is entirely
// well-formed. Outbound, an Edge whose field is empty must marshal to bytes with
// no target_graph key at all, or every existing collector's output changes shape
// the day this field ships.
func TestTargetGraph_PresentAndEmptyIsTheInGraphCase(t *testing.T) {
	const emptyValued = `{
		"walk_complete": true,
		"nodes": [{"id": "ISSUE-1", "type": "issue"}],
		"edges": [{"from_id": "ISSUE-1", "to_id": "ISSUE-2", "type": "blocks", "target_graph": ""}]
	}`

	// INBOUND: both gates admit it, and it converts as an ordinary edge.
	require.NoError(t, ValidateResultPayload("collect_graph", []byte(emptyValued)),
		"a present-but-blank target_graph is a conforming result")
	var structured any
	require.NoError(t, json.Unmarshal([]byte(emptyValued), &structured))
	r, err := DecodeResult("collect_graph", structured)
	require.NoError(t, err, "the strict decode admits the key whatever its value")
	require.Len(t, r.Edges, 1)
	assert.Empty(t, r.Edges[0].TargetGraph)

	cr, cerr := r.ToCollectResult("jira", "board")
	require.NoError(t, cerr)
	require.Len(t, cr.Edges, 1,
		"an empty value is the IN-GRAPH case: the edge belongs to the collect's own graph")
	assert.Equal(t, "ISSUE-2", cr.Edges[0].ToID)
	assert.Empty(t, r.CrossGraphEdges(),
		"and it reaches no cross-graph resolution, which would otherwise be asked to enumerate the family \"\"")

	// OUTBOUND: unset and explicitly-empty marshal to the SAME bytes, and
	// neither carries the key. This is what omitempty buys and the only
	// assertion that observes it.
	unset, uerr := json.Marshal(Edge{FromID: "A", ToID: "B", Type: "blocks"})
	require.NoError(t, uerr)
	blank, berr := json.Marshal(Edge{FromID: "A", ToID: "B", Type: "blocks", TargetGraph: ""})
	require.NoError(t, berr)
	assert.JSONEq(t, string(unset), string(blank),
		"absent and present-and-empty are indistinguishable on the way out")
	assert.NotContains(t, string(unset), "target_graph",
		"an edge that names no target graph marshals exactly as it did before this field existed")
}
