// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/enginetest"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// fakeGraphCaller is a package-local postpopulate.GraphCaller for the AWS
// PostPopulate family. It resolves cloud reads by Target.Account (a miss on an
// empty/unknown account) and records every create_batch / unlink mutation so a
// test can assert the write Target routes by Account (NOT Name). It replaces the
// real graph engine the resolvers used to take — the client holds no store engine.
type fakeGraphCaller struct {
	// nodesByAccount[account] = the cloud nodes present in that account graph.
	nodesByAccount map[string][]*knowledgev1.Node
	// edgesByFrom[fromID] = the outgoing edges of that node (BrowseEdges).
	edgesByFrom map[string][]knowledgev1.Edge

	mutations []*knowledgev1.ExecuteRequest
}

func (f *fakeGraphCaller) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	if m := req.GetMutation(); m != nil {
		f.mutations = append(f.mutations, req)
		return &knowledgev1.ExecuteResponse{}, nil
	}
	q := req.GetQuery()
	switch q.GetReturnMode() {
	case knowledgev1.ReturnMode_RETURN_MODE_GRAPH_NAMES:
		return f.graphNames(), nil
	case knowledgev1.ReturnMode_RETURN_MODE_EDGES:
		return f.edges(q), nil
	default:
		return f.browse(req.GetTarget(), q), nil
	}
}

// graphNames enumerates every account graph as a GraphInfo carrier (the
// RETURN_MODE_GRAPH_NAMES shape engine.DecodeGraphNames reads — only Name matters).
func (f *fakeGraphCaller) graphNames() *knowledgev1.ExecuteResponse {
	var infos []*knowledgev1.GraphInfo
	for acct := range f.nodesByAccount {
		infos = append(infos, &knowledgev1.GraphInfo{Name: acct})
	}
	return &knowledgev1.ExecuteResponse{GraphNames: infos}
}

// edges serves a RETURN_MODE_EDGES read for the (single) from-node id, filtered
// by the requested edge types.
func (f *fakeGraphCaller) edges(q *knowledgev1.QueryPlan) *knowledgev1.ExecuteResponse {
	ids := q.GetIds()
	if len(ids) == 0 {
		return &knowledgev1.ExecuteResponse{}
	}
	want := map[string]bool{}
	for _, t := range q.GetSelection().GetEdgeTypes() {
		want[t] = true
	}
	var out []*knowledgev1.Edge
	bucket := f.edgesByFrom[ids[0]]
	for i := range bucket {
		e := &bucket[i]
		if len(want) > 0 && !want[e.Type] {
			continue
		}
		out = append(out, &knowledgev1.Edge{
			FromId: e.FromId, ToId: e.ToId, Type: e.Type,
			Method: e.Method, Evidence: e.Evidence,
		})
	}
	return &knowledgev1.ExecuteResponse{Edges: out}
}

// browse serves a type-browse keyed by Target.Account, filtered by the
// resource_type metadata predicate (when present). An empty/unknown account is
// a miss (nil nodes).
func (f *fakeGraphCaller) browse(tgt *knowledgev1.GraphSelector, q *knowledgev1.QueryPlan) *knowledgev1.ExecuteResponse {
	acct := tgt.GetAccount()
	if acct == "" {
		return &knowledgev1.ExecuteResponse{} // miss on empty account.
	}
	nodes := f.nodesByAccount[acct]
	wantType := q.GetSelection().GetNodeType()
	wantRT := metadataEq(q.GetSelection().GetMetadataPredicates(), "resource_type")

	var matched []*knowledgev1.Node
	for _, n := range nodes {
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

// createBatchTargets returns the Target of every captured create_batch mutation.
func (f *fakeGraphCaller) createBatchTargets() []*knowledgev1.GraphSelector {
	var out []*knowledgev1.GraphSelector
	for _, req := range f.mutations {
		if req.GetMutation().GetKind() == knowledgev1.MutationPlan_MUTATION_KIND_CREATE {
			out = append(out, req.GetTarget())
		}
	}
	return out
}

// TestResolveSecurityGroupRules_RoutesByAccount drives the real
// resolveSecurityGroupRules through the wire fake and asserts the emitted
// reachability edges land in the per-account cloud graph (Target.Account ==
// the account graph name, NOT Target.Name) — the FUL-288 selector-field invariant.
func TestResolveSecurityGroupRules_RoutesByAccount(t *testing.T) {
	const acct = "111111111111"
	sg := &knowledgev1.Node{
		Id:         "arn:aws:ec2:us-east-1:" + acct + ":security-group/sg-1",
		Type:       string(kgtypes.NodeCloudResource),
		SymbolName: "sg-1",
		Content: `{"VpcId":"vpc-1","IpPermissions":[{"IpProtocol":"tcp","FromPort":443,"ToPort":443,` +
			`"IpRanges":[{"CidrIp":"10.0.0.0/8"}]}]}`,
	}
	kgtypes.SetValue(sg, "resource_type", "security-group")
	kgtypes.SetValue(sg, "region", "us-east-1")

	fc := &fakeGraphCaller{nodesByAccount: map[string][]*knowledgev1.Node{acct: {sg}}}

	require.NoError(t, resolveSecurityGroupRules(context.Background(), fc, acct))

	tgts := fc.createBatchTargets()
	require.NotEmpty(t, tgts, "expected at least one create_batch mutation (CIDR sentinel + edge)")
	for _, tgt := range tgts {
		assert.Equal(t, "cloud", tgt.GetGraph(), "edge write must target the cloud graph")
		assert.Equal(t, acct, tgt.GetAccount(), "SG edges must route by Account==%s (NOT Name)", acct)
		assert.Empty(t, tgt.GetName(), "cloud write must NOT route by Name")
	}
}

// TestResolveCrossAccountTrust_RemoteEdgesPerAccount asserts the batched
// cross-account write shape: one LinkEdgesBatch for the local graph and one per
// peer account graph, each routed by Target.Account.
func TestResolveCrossAccountTrust_RemoteEdgesPerAccount(t *testing.T) {
	const localAcct = "111111111111"
	const peerAcct = "222222222222"

	trustDoc := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow",` +
		`"Principal":{"AWS":"arn:aws:iam::` + peerAcct + `:role/peer-role"},"Action":"sts:AssumeRole"}]}`
	role := buildIAMRoleNode(t, "local-role", trustDoc)

	peerPrincipal := &knowledgev1.Node{
		Id:         "arn:aws:iam::" + peerAcct + ":role/peer-role",
		Type:       string(kgtypes.NodeCloudResource),
		SymbolName: "peer-role",
	}
	kgtypes.SetValue(peerPrincipal, "resource_type", "iam-role")

	fc := &fakeGraphCaller{nodesByAccount: map[string][]*knowledgev1.Node{
		localAcct: {role},
		peerAcct:  {peerPrincipal},
	}}

	require.NoError(t, resolveCrossAccountTrust(context.Background(), fc, localAcct))

	// Expect a TRUSTS edge written to BOTH the local and the peer account graph.
	seenAccounts := map[string]bool{}
	for _, tgt := range fc.createBatchTargets() {
		assert.Equal(t, "cloud", tgt.GetGraph())
		assert.Empty(t, tgt.GetName(), "trust write must NOT route by Name")
		seenAccounts[tgt.GetAccount()] = true
	}
	assert.True(t, seenAccounts[localAcct], "a TRUSTS edge must land in the local account graph")
	assert.True(t, seenAccounts[peerAcct], "a TRUSTS edge must land in the peer account graph")
}
