// SPDX-License-Identifier: Apache-2.0

package bitbucket

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/enginetest"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// fakeGraphCaller is a package-local postpopulate.GraphCaller for the Bitbucket
// PostPopulate family. It resolves cloud reads by Target.Account (a miss on an
// empty/unknown account) and records every create_batch mutation so a test can
// assert the federation-edge write routes into the cicd graph by Target.Account
// (NOT Name). It replaces the real store.DB the resolver used to take — no
// store.Init / store.Store().
type fakeGraphCaller struct {
	// nodesByAccount[account] = the cloud nodes present in that cloud account
	// graph (IAM roles + managed identities scanned for the Bitbucket issuer).
	nodesByAccount map[string][]*knowledgev1.Node

	mutations []*knowledgev1.ExecuteRequest
}

func (f *fakeGraphCaller) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	if m := req.GetMutation(); m != nil {
		_ = m
		f.mutations = append(f.mutations, req)
		return &knowledgev1.ExecuteResponse{}, nil
	}
	q := req.GetQuery()
	if q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_GRAPH_NAMES {
		return f.graphNames(), nil
	}
	return f.browse(req.GetTarget(), q), nil
}

func (f *fakeGraphCaller) graphNames() *knowledgev1.ExecuteResponse {
	var infos []*knowledgev1.GraphInfo
	for acct := range f.nodesByAccount {
		infos = append(infos, &knowledgev1.GraphInfo{Name: acct})
	}
	return &knowledgev1.ExecuteResponse{GraphNames: infos}
}

func (f *fakeGraphCaller) browse(tgt *knowledgev1.GraphSelector, q *knowledgev1.QueryPlan) *knowledgev1.ExecuteResponse {
	acct := tgt.GetAccount()
	if acct == "" {
		return &knowledgev1.ExecuteResponse{}
	}
	wantType := q.GetSelection().GetNodeType()
	wantRT := metadataEq(q.GetSelection().GetMetadataPredicates(), "resource_type")

	var matched []*knowledgev1.Node
	for _, n := range f.nodesByAccount[acct] {
		if wantType != "" && n.GetType() != wantType {
			continue
		}
		if wantRT != "" && kgtypes.Value(n, "resource_type") != wantRT {
			continue
		}
		matched = append(matched, n)
	}
	return enginetest.ResponseWithNodes(matched...)
}

func metadataEq(preds []*knowledgev1.MetadataPredicate, key string) string {
	for _, p := range preds {
		if p.GetKey() == key && p.GetOp() == knowledgev1.MetadataPredicate_OP_EQ {
			return p.GetValue()
		}
	}
	return ""
}

func (f *fakeGraphCaller) createBatchTargets() []*knowledgev1.GraphSelector {
	var out []*knowledgev1.GraphSelector
	for _, req := range f.mutations {
		if req.GetMutation().GetKind() == knowledgev1.MutationPlan_MUTATION_KIND_CREATE {
			out = append(out, req.GetTarget())
		}
	}
	return out
}

func (f *fakeGraphCaller) createBatchEdgeToIDs() []string {
	var out []string
	for _, req := range f.mutations {
		m := req.GetMutation()
		if m.GetKind() != knowledgev1.MutationPlan_MUTATION_KIND_CREATE {
			continue
		}
		for _, e := range m.GetEdges() {
			out = append(out, e.GetToId())
		}
	}
	return out
}

// TestBitbucketPostPopulate_RoutesByAccount drives the real bitbucketPostPopulate
// through the wire fake: a cloud IAM role whose trust policy references the
// Bitbucket Pipelines OIDC issuer must produce an EdgeFederates edge written into
// the cicd Bitbucket graph by Target.Account == "bitbucket-<workspace>" (NOT
// Target.Name) — the FUL-288 selector-field invariant for the cicd family.
func TestBitbucketPostPopulate_RoutesByAccount(t *testing.T) {
	const (
		cloudAcct = "123456789012"
		workspace = "acme"
		cicdGraph = "bitbucket-" + workspace
		roleARN   = "arn:aws:iam::123456789012:role/bitbucket-deploy"
	)

	issuerURL := bitbucketIssuerURL(workspace)
	role := &knowledgev1.Node{
		Id:         roleARN,
		Type:       string(kgtypes.NodeCloudResource),
		SymbolName: "bitbucket-deploy",
		Content:    `{"Federated":"` + issuerURL + `"}`,
	}
	kgtypes.SetValue(role, "resource_type", "iam-role")

	fc := &fakeGraphCaller{nodesByAccount: map[string][]*knowledgev1.Node{
		cloudAcct: {role},
	}}

	require.NoError(t, bitbucketPostPopulate(context.Background(), fc, cicdGraph))

	tgts := fc.createBatchTargets()
	require.NotEmpty(t, tgts, "expected at least one create_batch federation-edge mutation")
	for _, tgt := range tgts {
		assert.Equal(t, "cicd", tgt.GetGraph(), "federation edge write must target the cicd graph")
		assert.Equal(t, cicdGraph, tgt.GetAccount(), "bitbucket OIDC edges must route by Account==%s (NOT Name)", cicdGraph)
		assert.Empty(t, tgt.GetName(), "cicd write must NOT route by Name")
	}
	assert.Contains(t, fc.createBatchEdgeToIDs(), roleARN,
		"the federation edge must point at the trusted IAM role")
}

// TestBitbucketPostPopulate_NonBitbucketGraph_NoOp confirms a cicd graph whose
// name does not carry the "bitbucket-" prefix (e.g. a github graph) is a silent
// no-op — no cloud scan, no write.
func TestBitbucketPostPopulate_NonBitbucketGraph_NoOp(t *testing.T) {
	const cloudAcct = "123456789012"
	role := &knowledgev1.Node{
		Id:         "arn:aws:iam::123456789012:role/r",
		Type:       string(kgtypes.NodeCloudResource),
		SymbolName: "r",
		Content:    `{"Federated":"` + bitbucketIssuerURL("acme") + `"}`,
	}
	kgtypes.SetValue(role, "resource_type", "iam-role")
	fc := &fakeGraphCaller{nodesByAccount: map[string][]*knowledgev1.Node{
		cloudAcct: {role},
	}}

	require.NoError(t, bitbucketPostPopulate(context.Background(), fc, "myorg"))
	assert.Empty(t, fc.createBatchTargets(), "non-bitbucket cicd graph must not write any federation edges")
}

// TestResolveOIDCFederation_AzureFederatedIdentity covers the Azure managed-
// identity branch: a managed identity with a federated credential matching the
// Bitbucket issuer produces one EdgeFederates edge into the cicd graph.
func TestResolveOIDCFederation_AzureFederatedIdentity(t *testing.T) {
	const (
		cloudAcct = "sub-xyz"
		workspace = "acme"
		cicdGraph = "bitbucket-" + workspace
		miID      = "/subscriptions/sub-xyz/resourceGroups/rg/providers/Microsoft.ManagedIdentity/userAssignedIdentities/mi"
	)
	issuerURL := bitbucketIssuerURL(workspace)
	mi := &knowledgev1.Node{
		Id:         miID,
		Type:       string(kgtypes.NodeCloudResource),
		SymbolName: "mi",
		Content:    `{"federated_credentials":[{"issuer":"` + issuerURL + `","subject":"repo:acme/api"}]}`,
	}
	kgtypes.SetValue(mi, "resource_type", "managed-identity")
	fc := &fakeGraphCaller{nodesByAccount: map[string][]*knowledgev1.Node{
		cloudAcct: {mi},
	}}

	require.NoError(t, resolveOIDCFederation(context.Background(), fc, cicdGraph, workspace))

	require.Contains(t, fc.createBatchEdgeToIDs(), miID,
		"federation edge must point at the managed identity")
	for _, tgt := range fc.createBatchTargets() {
		assert.Equal(t, cicdGraph, tgt.GetAccount())
	}
}
