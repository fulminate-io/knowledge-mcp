// SPDX-License-Identifier: Apache-2.0

package github

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/enginetest"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// fakeGraphCaller is a package-local postpopulate.GraphCaller for the GitHub
// PostPopulate family. It resolves cloud reads by Target.Account (a miss on an
// empty/unknown account) and records every create_batch mutation so a test can
// assert the federation-edge write routes into the cicd graph by Target.Account
// (NOT Name). It replaces the real store.DB the resolver used to take — no
// store.Init / store.Store().
type fakeGraphCaller struct {
	// nodesByAccount[account] = the cloud nodes present in that cloud account
	// graph (IAM roles scanned for GitHub OIDC trust).
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

// graphNames enumerates every cloud account graph as a GraphInfo carrier.
func (f *fakeGraphCaller) graphNames() *knowledgev1.ExecuteResponse {
	var infos []*knowledgev1.GraphInfo
	for acct := range f.nodesByAccount {
		infos = append(infos, &knowledgev1.GraphInfo{Name: acct})
	}
	return &knowledgev1.ExecuteResponse{GraphNames: infos}
}

// browse serves a type-browse keyed by Target.Account, filtered by the
// resource_type predicate (when present). An empty/unknown account is a miss.
func (f *fakeGraphCaller) browse(tgt *knowledgev1.GraphSelector, q *knowledgev1.QueryPlan) *knowledgev1.ExecuteResponse {
	acct := tgt.GetAccount()
	if acct == "" {
		return &knowledgev1.ExecuteResponse{}
	}
	wantType := q.GetSelection().GetNodeType()
	wantRT := metadataEq(q.GetSelection().GetMetadataPredicates(), "resource_type")

	var matched []*knowledgev1.Node
	for _, n := range f.nodesByAccount[acct] {
		if wantType != "" && n.Type != wantType {
			continue
		}
		if wantRT != "" && kgtypes.Value(n, "resource_type") != wantRT {
			continue
		}
		matched = append(matched, n)
	}
	return enginetest.ResponseWithNodes(matched...)
}

// metadataEq returns the OP_EQ value for key from a predicate set, or "".
func metadataEq(preds []*knowledgev1.MetadataPredicate, key string) string {
	for _, p := range preds {
		if p.GetKey() == key && p.GetOp() == knowledgev1.MetadataPredicate_OP_EQ {
			return p.GetValue()
		}
	}
	return ""
}

// createBatchEdges returns every edge carried by the captured create_batch
// mutations, paired with the Target the mutation routed to.
func (f *fakeGraphCaller) createBatchTargets() []*knowledgev1.GraphSelector {
	var out []*knowledgev1.GraphSelector
	for _, req := range f.mutations {
		if req.GetMutation().GetKind() == knowledgev1.MutationPlan_MUTATION_KIND_CREATE {
			out = append(out, req.GetTarget())
		}
	}
	return out
}

// createBatchEdgeToIDs returns the to_id of every edge in the captured
// create_batch mutations.
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

// TestPostPopulateOIDC_RoutesByAccount drives the real postPopulateOIDC through
// the wire fake: a cloud IAM role trusting the GitHub Actions OIDC provider must
// produce an EdgeFederates edge written into the cicd GitHub graph by
// Target.Account == the cicd graph name (NOT Target.Name) — the
// selector-field invariant for the cicd family.
func TestPostPopulateOIDC_RoutesByAccount(t *testing.T) {
	const (
		cloudAcct = "123456789012"
		cicdGraph = "myorg"
		roleARN   = "arn:aws:iam::123456789012:role/deploy-role"
	)

	trustPolicyJSON := `{
		"AssumeRolePolicyDocument": "{\"Version\":\"2012-10-17\",\"Statement\":[{\"Effect\":\"Allow\",\"Principal\":{\"Federated\":\"arn:aws:iam::123456789012:oidc-provider/token.actions.githubusercontent.com\"},\"Action\":\"sts:AssumeRoleWithWebIdentity\",\"Condition\":{\"StringLike\":{\"token.actions.githubusercontent.com:sub\":\"repo:myorg/api:*\"}}}]}"
	}`
	role := &knowledgev1.Node{
		Id:         roleARN,
		Type:       string(kgtypes.NodeCloudResource),
		SymbolName: "deploy-role",
		Content:    trustPolicyJSON,
	}
	kgtypes.SetValue(role, "resource_type", "iam-role")

	fc := &fakeGraphCaller{nodesByAccount: map[string][]*knowledgev1.Node{cloudAcct: {role}}}

	require.NoError(t, postPopulateOIDC(context.Background(), fc, cicdGraph))

	tgts := fc.createBatchTargets()
	require.NotEmpty(t, tgts, "expected at least one create_batch federation-edge mutation")
	for _, tgt := range tgts {
		assert.Equal(t, "cicd", tgt.GetGraph(), "federation edge write must target the cicd graph")
		assert.Equal(t, cicdGraph, tgt.GetAccount(), "github OIDC edges must route by Account==%s (NOT Name)", cicdGraph)
		assert.Empty(t, tgt.GetName(), "cicd write must NOT route by Name")
	}
	assert.Contains(t, fc.createBatchEdgeToIDs(), roleARN,
		"the federation edge must point at the trusted IAM role")
}

// TestPostPopulateOIDC_NoMatch_NoWrite confirms that when no cloud IAM role
// trusts the GitHub OIDC provider, no create_batch mutation is fired (the empty
// edges no-op in LinkEdgesBatch).
func TestPostPopulateOIDC_NoMatch_NoWrite(t *testing.T) {
	const cloudAcct = "123456789012"
	role := &knowledgev1.Node{
		Id:         "arn:aws:iam::123456789012:role/other-role",
		Type:       string(kgtypes.NodeCloudResource),
		SymbolName: "other-role",
		Content:    `{"AssumeRolePolicyDocument":"{\"Version\":\"2012-10-17\",\"Statement\":[]}"}`,
	}
	kgtypes.SetValue(role, "resource_type", "iam-role")

	fc := &fakeGraphCaller{nodesByAccount: map[string][]*knowledgev1.Node{cloudAcct: {role}}}

	require.NoError(t, postPopulateOIDC(context.Background(), fc, "myorg"))
	assert.Empty(t, fc.createBatchTargets(), "no trusting role → no federation-edge write")
}
