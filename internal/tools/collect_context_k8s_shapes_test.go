// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/externalcollector"
)

// collect_context_k8s_shapes_test.go — R5: THE DECLARED BLOCK CARRIES THE
// FOREIGN INPUTS THE COMPILED-IN LINKER'S EDGE SHAPES READ, and the two that
// read none still work with no block at all.
//
// WHAT THIS TICKET OWES AND WHAT IT DOES NOT. R5's EMISSION half — the k8s
// module writing those edges through the target-graph field — is ticket 20's
// field, which is not in this tree. Those rows are RED-PENDING and named as such
// at the bottom of this file rather than stubbed green. What is provable here,
// and what every one of those rows will stand on, is that the block CARRIES the
// inputs: a slice that looks right and links nothing is the failure mode the
// helm shape already demonstrated once.
//
// EACH ROW IS KEYED TO THE PREDICATE THAT CONSUMES IT, read in the linker this
// session, and each derivation below is written the way that predicate reads its
// input — a fixture that derived something easier would prove something easier.

// TestCollectContext_ANamesOnlyDeclarationCarriesNamesAndCostsNoBrowse is the
// FILL's own row: a declaration naming the code family with NO node types at
// all yields the graph names and nothing else, and issues no node browse.
//
// IT IS NOT KEYED TO A PREDICATE ANY MORE. It was written for the tier1-image
// membership test on image basenames, and the k8s collector no longer emits
// that edge, so the row now stands on what it always actually observed: the
// client's fill behavior for an entry that asks for a family and no nodes,
// which the contract still admits and any module may still declare.
func TestCollectContext_ANamesOnlyDeclarationCarriesNamesAndCostsNoBrowse(t *testing.T) {
	caller := chartFileCaller()
	// A branch-overlay graph name, which the linker's own set builder DROPS.
	caller.names["code"] = append(caller.names["code"], "api@feature-x")

	deps := &customDeps{graphs: caller}
	block, _, err := fillCollectContext(context.Background(), deps,
		externalcollector.ContextDeclaration{externalcollector.ContextFamilyCode: {}})
	require.NoError(t, err)
	require.NotNil(t, block)

	// THE DROP IS THE FILL'S, NOT THE CONSUMER'S. Asserted over the BLOCK rather
	// than over a derived set, because a module reading the block at face value
	// is the population this protects: the linker's own chart predicate drops @
	// names (linker/helm.go:117 inside buildChartMap), so a block carrying one
	// would give a module a different answer from the same-looking input.
	for _, g := range (*block)[externalcollector.ContextFamilyCode] {
		assert.NotContains(t, g.GraphName, "@",
			"a branch-overlay graph name must not reach the module")
	}

	names := codeGraphNamesFrom(*block)
	assert.True(t, names["api"], "an indexed graph's name is in the set")
	assert.True(t, names["web"])
	assert.False(t, names["absent"], "and a name no graph carries is not")
	assert.False(t, names["api@feature-x"],
		"and the consumer-side control agrees: the overlay name is nowhere in the set")

	// THE COST CELL: a declaration naming no node types issues NO browse. That
	// is what makes "graph names only" a real declaration rather than a full read
	// with the nodes thrown away.
	assert.Zero(t, caller.browseCount(), "no node type declared, so no node browse ran")
	// THE CONTROL for that zero, same fake and same counter.
	_, _, err = fillCollectContext(context.Background(), deps,
		externalcollector.ContextDeclaration{externalcollector.ContextFamilyCode: {
			NodeTypes: []string{"file"}, NodeFields: []string{externalcollector.ContextNodeFieldID},
		}})
	require.NoError(t, err)
	assert.Equal(t, 2, caller.browseCount(),
		"one browse per REAL code graph: the overlay name costs neither a browse nor an entry")
}

// codeGraphNamesFrom derives the code-graph name set a names-only declaration
// yields, applying the same overlay-name drop the linker applies.
func codeGraphNamesFrom(block externalcollector.CollectContext) map[string]bool {
	code := block[externalcollector.ContextFamilyCode]
	out := make(map[string]bool, len(code))
	for _, g := range code {
		if strings.Contains(g.GraphName, "@") {
			continue
		}
		out[g.GraphName] = true
	}
	return out
}

// TestK8sShape_DeploysNeedsTheChartFileNodeIDAndItsBody is the tier1-helm
// input, and the cell that matters is the LAST one: a block carrying chart
// NAMES alone yields no edge, because the edge's FROM endpoint is the Chart.yaml
// file node's own id.
func TestK8sShape_DeploysNeedsTheChartFileNodeIDAndItsBody(t *testing.T) {
	caller := chartFileCaller()
	caller.graphs["code"]["api"] = append(caller.graphs["code"]["api"],
		// A chart whose body carries no name: line — the linker falls back to the
		// parent directory basename.
		codeFileNode("api:nameless", "charts/legacy/Chart.yaml", "apiVersion: v2\n"))

	deps := &customDeps{graphs: caller}
	block, _, err := fillCollectContext(context.Background(), deps,
		externalcollector.ContextDeclaration{externalcollector.ContextFamilyCode: {
			NodeTypes: []string{"file"},
			NodeFields: []string{
				externalcollector.ContextNodeFieldID,
				externalcollector.ContextNodeFieldFilePath,
				externalcollector.ContextNodeFieldContent,
			},
			PathBasenames: []string{"Chart.yaml", "Chart.yml"},
		}})
	require.NoError(t, err)

	charts := chartMapFrom(*block)

	// A body with a name: line.
	require.Contains(t, charts, "api-chart")
	assert.Equal(t, "api:chart", charts["api-chart"],
		"the DEPLOYS edge's FROM endpoint is the Chart.yaml FILE NODE'S id")
	require.Contains(t, charts, "web-chart")
	assert.Equal(t, "web:chart", charts["web-chart"])

	// A body with no name: line, where the linker falls back to the parent
	// directory basename.
	require.Contains(t, charts, "legacy")
	assert.Equal(t, "api:nameless", charts["legacy"])

	// THE BASENAME NARROWING DID ITS WORK: the two non-chart files in the fixture
	// are absent, so the declaration narrowed rather than being carried and
	// ignored.
	for _, g := range (*block)[externalcollector.ContextFamilyCode] {
		for _, n := range g.Nodes {
			assert.Contains(t, []string{"Chart.yaml", "Chart.yml"}, filepath.Base(n.FilePath),
				"path_basenames narrowed the slice: %s should not be here", n.FilePath)
		}
	}

	// THE CELL THE HELM BOUND EXISTS FOR: a slice carrying names and no node ids
	// yields NO edge. It is the cheapest way to catch a declaration that looks
	// right and links nothing.
	namesOnly, _, err := fillCollectContext(context.Background(), deps,
		externalcollector.ContextDeclaration{externalcollector.ContextFamilyCode: {
			NodeTypes:     []string{"file"},
			NodeFields:    []string{externalcollector.ContextNodeFieldContent},
			PathBasenames: []string{"Chart.yaml", "Chart.yml"},
		}})
	require.NoError(t, err)
	for name, id := range chartMapFrom(*namesOnly) {
		assert.Empty(t, id, "chart %q was found by name and carries no endpoint to link from", name)
	}
}

// chartMapFrom derives the chart-name to node-id map the DEPLOYS predicate
// builds, applying the linker's own parse and its parent-directory fallback.
func chartMapFrom(block externalcollector.CollectContext) map[string]string {
	out := map[string]string{}
	for _, g := range block[externalcollector.ContextFamilyCode] {
		if strings.Contains(g.GraphName, "@") {
			continue
		}
		for _, n := range g.Nodes {
			base := filepath.Base(n.FilePath)
			if base != "Chart.yaml" && base != "Chart.yml" {
				continue
			}
			name := chartNameFromBody(n.Content)
			if name == "" {
				name = filepath.Base(filepath.Dir(n.FilePath))
			}
			if name == "" || name == "." {
				continue
			}
			out[name] = n.ID
		}
	}
	return out
}

// chartNameFromBody reads the chart name out of a Chart.yaml body, the way the
// linker's own extractor does.
func chartNameFromBody(content string) string {
	for line := range strings.SplitSeq(content, "\n") {
		if after, ok := strings.CutPrefix(strings.TrimSpace(line), "name:"); ok {
			return strings.Trim(strings.TrimSpace(after), `"'`)
		}
	}
	return ""
}

// TestK8sShape_AzureWorkloadIdentityNeedsIDAndClientID is the tier1-azure-wi
// input: per azure graph, every resource node's id and its clientId.
//
// THE FAMILY IS THE REGISTERED `azure` TYPE, not the retired `cloud` builtin.
// That is the whole substance of the k8s module's declaration change: the shape
// the predicate needs is unchanged, and the name it reads that shape out of is
// now a graph type an operator registered.
func TestK8sShape_AzureWorkloadIdentityNeedsIDAndClientID(t *testing.T) {
	deps := azureIdentityDeps()
	block, _, err := fillCollectContext(context.Background(), deps,
		externalcollector.ContextDeclaration{"azure": {
			NodeTypes:    []string{"azure-resource"},
			NodeFields:   []string{externalcollector.ContextNodeFieldID},
			MetadataKeys: []string{"clientId"},
		}})
	require.NoError(t, err)

	assert.Equal(t, "mi/app", azureIdentityFor(*block, "cid-1"), "a matching clientId resolves to the node id")
	assert.Empty(t, azureIdentityFor(*block, "cid-absent"), "a non-matching one resolves to nothing")

	// THE KEY-ABSENT CELL: the identity node carrying no clientId is in the
	// slice and matches nothing, rather than matching the empty string.
	assert.Empty(t, azureIdentityFor(*block, ""), "the empty clientId matches no node")

	// AND THE UNDECLARED-KEY CELL: the same fixture with clientId undeclared
	// carries the nodes and resolves nothing.
	blind, _, err := fillCollectContext(context.Background(), deps,
		externalcollector.ContextDeclaration{"azure": {
			NodeTypes:  []string{"azure-resource"},
			NodeFields: []string{externalcollector.ContextNodeFieldID},
		}})
	require.NoError(t, err)
	require.NotEmpty(t, (*blind)["azure"][0].Nodes, "fixture control: the nodes are still carried")
	assert.Empty(t, azureIdentityFor(*blind, "cid-1"), "with clientId undeclared, nothing resolves")
}

// azureIdentityDeps wires one registered azure graph holding two managed
// identities, one of which carries no clientId.
func azureIdentityDeps() *customDeps {
	return &customDeps{
		crud: registeredCRUD("azure"),
		graphs: &contextGraphCaller{
			names: map[string][]string{"azure": {"azure-prod"}},
			graphs: map[string]map[string][]*knowledgev1.Node{"azure": {"azure-prod": {
				{Id: "mi/app", Type: "azure-resource", SymbolName: "app-identity",
					Metadata: map[string]string{"clientId": "cid-1", "resource_type": "azure:managed-identity"}},
				{Id: "mi/other", Type: "azure-resource", SymbolName: "other",
					Metadata: map[string]string{"resource_type": "azure:managed-identity"}},
			}}},
		},
	}
}

// azureIdentityFor derives the lookup the WORKLOAD_IDENTITY predicate performs:
// scan every azure graph for a node whose clientId matches, return its id.
func azureIdentityFor(block externalcollector.CollectContext, clientID string) string {
	if clientID == "" {
		return ""
	}
	for _, g := range block["azure"] {
		for _, n := range g.Nodes {
			if n.Metadata["clientId"] == clientID {
				return n.ID
			}
		}
	}
	return ""
}

// TestK8sShape_NoDeclarationYieldsNoBlockAtAll is R5's NEGATIVE CONTROL, kept at
// exactly what it observes in production.
//
// THE TWO SHAPES IT STANDS FOR read no foreign graph at all: tier1-irsa takes
// its target verbatim from the ServiceAccount's own irsa_role_arn metadata, and
// tier1-gcp-wi composes its target from the SA's own gcp_service_account
// annotation. Neither reads a foreign graph, so neither can be regressed by a
// context block — what WOULD regress them is a fill that made a block mandatory,
// and that is the one thing this row can observe from here. Both predicates now
// live in the k8s contrib collector rather than in the compiled-in linker, which
// changes where they are, not what they read.
//
// WHY IT ASSERTS SO LITTLE, said rather than padded. An earlier version of this
// row called two helper functions declared in this same file and asserted they
// returned the values they were written to return. That is a subject supplying
// its own answer key: deleting both linker shapes outright would have left it
// green. The helpers are gone. Observing the linker's own two shapes needs a row
// in the linker package driving them through a recording emitter, which is
// linker work this ticket does not otherwise touch.
func TestK8sShape_NoDeclarationYieldsNoBlockAtAll(t *testing.T) {
	// A nil declaration: no block, and the argument key is omitted entirely.
	block, _, err := fillCollectContext(context.Background(), &customDeps{}, nil)
	require.NoError(t, err)
	require.Nil(t, block, "an entry declaring nothing receives no block at all")

	// An EMPTY declaration is the same answer by the same route, and it is the
	// shape a config entry carrying `"context": {}` produces.
	block, _, err = fillCollectContext(context.Background(), &customDeps{},
		externalcollector.ContextDeclaration{})
	require.NoError(t, err)
	require.Nil(t, block)

	// THE CONTROL, in the same run and through the same call: a real declaration
	// against a wired seam DOES yield a block, so the two nils above are a
	// decision rather than a fill that returns nil for everything.
	deps := &customDeps{crud: registeredCRUD("aws"), graphs: twoResourceCaller()}
	block, _, err = fillCollectContext(context.Background(), deps,
		externalcollector.ContextDeclaration{"aws": {
			NodeTypes:  []string{"aws-resource"},
			NodeFields: []string{externalcollector.ContextNodeFieldID},
		}})
	require.NoError(t, err)
	require.NotNil(t, block)
	assert.Len(t, (*block)["aws"], 1)
}
