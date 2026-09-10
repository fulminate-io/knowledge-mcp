// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/externalcollector"
)

// collect_context_k8s_emission_test.go — R5's EMISSION half, split out of
// collect_context_k8s_shapes_test.go when that file passed the length gate's
// warning line. Its sibling proves the declared block CARRIES the predicates'
// inputs; this proves what a module derives from them is expressible as a
// cross-graph edge and survives the contract's own partition.

// --- R5's EMISSION HALF, against the real target-graph field ---
//
// THE ROWS ABOVE PROVE THE BLOCK CARRIES THE INPUTS. These prove the other half
// of R5: that what a module derives from those inputs is EXPRESSIBLE as a
// cross-graph edge and survives the contract's own partition with its endpoints,
// its type and its tier intact. Between the two, a module has everything it needs
// and nothing it has to invent.
//
// THE FAMILY IS A FAMILY, NEVER AN INSTANCE, which is what makes it derivable
// from the block at all: the module knows which ARM of the block an endpoint
// came from, and that arm IS the family. It never has to name a graph instance,
// and the client enumerates the family and locates the endpoint itself.
//
// WHICH OF THE TWO FIELDS CARRIES IT SAYS WHICH END IS FOREIGN. The Helm shape
// runs FROM the chart file node, so its family goes in SourceGraph; the
// identity shape runs FROM the collector's own ServiceAccount, so its family
// goes in TargetGraph.
//
// THE TIERS AND CONFIDENCES ARE THE BUILT-IN LINKER'S, read at
// cmd/knowledge/internal/linker/helm.go:81 (DEPLOYS, tier1-helm, 0.85) and
// workload_identity.go:125 (WORKLOAD_IDENTITY, tier1-azure-wi, 0.9). A module
// emitting different ones would produce edges an operator could not compare
// against a built-in collect.

// k8sDerivedEdges is the module's emission, derived from the block by the same
// predicates the rows above exercise. Each edge names the family its FOREIGN
// endpoint came from, and nothing else about it.
func k8sDerivedEdges(block externalcollector.CollectContext) []externalcollector.Edge {
	var out []externalcollector.Edge

	// DEPLOYS: the chart's FILE NODE id is the endpoint, not the chart name, and
	// it is the edge's FROM — so the family rides SourceGraph, which is the
	// direction the k8s collector emits.
	if id := chartMapFrom(block)["api-chart"]; id != "" {
		out = append(out, externalcollector.Edge{
			FromID: id, ToID: "Deployment/api", Type: "DEPLOYS",
			SourceGraph: externalcollector.ContextFamilyCode,
			Method:      "tier1-helm", Confidence: 0.85,
		})
	}
	// WORKLOAD_IDENTITY: the azure identity whose clientId the ServiceAccount names.
	if id := azureIdentityFor(block, "cid-1"); id != "" {
		out = append(out, externalcollector.Edge{
			FromID: "ServiceAccount/api", ToID: id, Type: "WORKLOAD_IDENTITY",
			TargetGraph: "azure",
			Method:      "tier1-azure-wi", Confidence: 0.9,
		})
	}
	return out
}

// k8sEmissionBlock fills a block carrying every input the two block-fed shapes
// read.
func k8sEmissionBlock(t *testing.T) externalcollector.CollectContext {
	t.Helper()
	// ONE CALLER SERVES BOTH FAMILIES, which is what the fill now expects: the
	// code arm and the registered-azure arm ride the same Execute carrier, so a
	// fixture wiring two seams would be describing a client that no longer exists.
	caller := chartFileCaller()
	azure := azureIdentityDeps()
	caller.names["azure"] = azure.graphs.(*contextGraphCaller).names["azure"]
	caller.graphs["azure"] = azure.graphs.(*contextGraphCaller).graphs["azure"]
	deps := &customDeps{graphs: caller, crud: registeredCRUD("azure")}
	block, _, err := fillCollectContext(context.Background(), deps, externalcollector.ContextDeclaration{
		externalcollector.ContextFamilyCode: {
			NodeTypes: []string{"file"},
			NodeFields: []string{
				externalcollector.ContextNodeFieldID,
				externalcollector.ContextNodeFieldFilePath,
				externalcollector.ContextNodeFieldContent,
			},
			PathBasenames: []string{"Chart.yaml", "Chart.yml"},
		},
		"azure": {
			NodeTypes:    []string{"azure-resource"},
			NodeFields:   []string{externalcollector.ContextNodeFieldID},
			MetadataKeys: []string{"clientId"},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, block)
	return *block
}

// TestK8sShape_TheTwoShapesEmitThroughTheGraphFamilyFields is R5's emission
// row. It drives the derived edges through the contract's own partition, which
// is the seam a real module's result crosses.
func TestK8sShape_TheTwoShapesEmitThroughTheGraphFamilyFields(t *testing.T) {
	edges := k8sDerivedEdges(k8sEmissionBlock(t))
	require.Len(t, edges, 2, "both block-fed shapes derived an edge from the block")

	// The partition is the contract's, run over a result shaped as a provider
	// would return it.
	result := &externalcollector.Result{Edges: edges, WalkComplete: true}
	cross := result.CrossGraphEdges()
	require.Len(t, cross, 2, "every derived edge names one of the two family fields, so every one is cross-graph")

	byType := map[string]externalcollector.CrossGraphEdge{}
	for _, e := range cross {
		byType[e.Type] = e
	}

	assert.Equal(t, externalcollector.CrossGraphEdge{
		SourceGraph: "code", FromID: "api:chart", ToID: "Deployment/api",
		Type: "DEPLOYS", Method: "tier1-helm", Confidence: 0.85,
	}, byType["DEPLOYS"], "the DEPLOYS endpoint is the Chart.yaml FILE NODE's id, which is why the block carries ids, "+
		"and it is the FROM, which is why the family rides source_graph")

	assert.Equal(t, externalcollector.CrossGraphEdge{
		TargetGraph: "azure", FromID: "ServiceAccount/api", ToID: "mi/app",
		Type: "WORKLOAD_IDENTITY", Method: "tier1-azure-wi", Confidence: 0.9,
	}, byType["WORKLOAD_IDENTITY"])
}

// TestK8sShape_AnEdgeNamingNoTargetGraphStaysInTheCollectsOwnGraph is the
// partition's other side, and the control for the row above: the same result
// carrying an ordinary in-graph edge keeps it out of the cross-graph half. A
// partition that lifted everything would satisfy every assertion above.
func TestK8sShape_AnEdgeNamingNoTargetGraphStaysInTheCollectsOwnGraph(t *testing.T) {
	edges := append(k8sDerivedEdges(k8sEmissionBlock(t)), externalcollector.Edge{
		FromID: "Deployment/api", ToID: "Pod/api-1", Type: "OWNS",
	})
	result := &externalcollector.Result{Edges: edges, WalkComplete: true}

	cross := result.CrossGraphEdges()
	assert.Len(t, cross, 2, "the in-graph edge is not lifted")
	for _, e := range cross {
		assert.NotEqual(t, "OWNS", e.Type)
	}

	// AND IT REACHES THE COLLECT'S OWN GRAPH, which is the half the partition
	// owes the other way: nothing the provider emitted is dropped by the split.
	collected, err := result.ToCollectResult("k8s", "prod")
	require.NoError(t, err)
	var sawOwns bool
	for _, e := range collected.Edges {
		if e.Type == "OWNS" {
			sawOwns = true
		}
	}
	assert.True(t, sawOwns, "the in-graph edge lands in the collect's own graph")
}

// TestK8sShape_TheTwoNoInputShapesNeedNoBlockToEmit is R5's negative control on
// the emission side. tier1-irsa and tier1-gcp-wi take their targets from the
// ServiceAccount's own metadata (linker/workload_identity.go:66-76 and :95-110),
// so a module emitting them derives nothing from the block — and their edges are
// cross-graph on the same terms, because their endpoints are cloud identities.
//
// Without this row a module that required a block before emitting anything would
// pass every row above and regress two shapes.
func TestK8sShape_TheTwoNoInputShapesNeedNoBlockToEmit(t *testing.T) {
	empty := externalcollector.CollectContext{}
	assert.Empty(t, k8sDerivedEdges(empty),
		"fixture control: with an empty block the two block-fed shapes derive nothing")

	// The two that read no foreign graph still emit, from the ServiceAccount alone.
	result := &externalcollector.Result{
		Edges: []externalcollector.Edge{
			{
				FromID: "ServiceAccount/api", ToID: "arn:aws:iam::1234:role/app",
				Type: "WORKLOAD_IDENTITY", TargetGraph: "aws",
				Method: "tier1-irsa", Confidence: 1.0,
			},
			{
				FromID: "ServiceAccount/api",
				ToID:   "projects/proj-x/serviceAccounts/app@proj-x.iam.gserviceaccount.com",
				Type:   "WORKLOAD_IDENTITY", TargetGraph: "gcp",
				Method: "tier1-gcp-wi", Confidence: 1.0,
			},
		},
		WalkComplete: true,
	}
	cross := result.CrossGraphEdges()
	require.Len(t, cross, 2, "both emit with no block in play")
	assert.Equal(t, "tier1-irsa", cross[0].Method)
	assert.Equal(t, "tier1-gcp-wi", cross[1].Method)
}
