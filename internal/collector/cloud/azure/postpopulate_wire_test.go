// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/enginetest"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// fakeGraphCaller is a package-local postpopulate.GraphCaller for the Azure
// PostPopulate family. Resolves cloud reads by Target.Account (miss on empty),
// serves outgoing/incoming edge reads, and records create_batch / unlink
// mutations so a test can assert writes route by Account (NOT Name). Replaces the
// real graph engine — the client holds no store engine.
type fakeGraphCaller struct {
	nodesByAccount map[string][]*knowledgev1.Node
	outEdges       map[string][]knowledgev1.Edge
	inEdges        map[string][]knowledgev1.Edge

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
		var infos []*knowledgev1.GraphInfo
		for a := range f.nodesByAccount {
			infos = append(infos, &knowledgev1.GraphInfo{Name: a})
		}
		return &knowledgev1.ExecuteResponse{GraphNames: infos}, nil
	case knowledgev1.ReturnMode_RETURN_MODE_EDGES:
		return f.edges(q), nil
	default:
		return f.browse(req.GetTarget(), q), nil
	}
}

func (f *fakeGraphCaller) edges(q *knowledgev1.QueryPlan) *knowledgev1.ExecuteResponse {
	ids := q.GetIds()
	if len(ids) == 0 {
		return &knowledgev1.ExecuteResponse{}
	}
	set := f.outEdges
	if !q.GetForward() {
		set = f.inEdges
	}
	want := map[string]bool{}
	for _, t := range q.GetSelection().GetEdgeTypes() {
		want[t] = true
	}
	var out []*knowledgev1.Edge
	srcEdges := set[ids[0]]
	for i := range srcEdges {
		e := &srcEdges[i]
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

// TestResolveNSGRules_RoutesByAccount drives the real resolveNSGRules through
// the wire fake and asserts the emitted reachability edges + CIDR sentinel nodes
// land in the per-subscription cloud graph (Target.Account == subscription, NOT
// Target.Name).
func TestResolveNSGRules_RoutesByAccount(t *testing.T) {
	const sub = "00000000-0000-0000-0000-000000000001"
	nsg := &knowledgev1.Node{
		Id:         "/subscriptions/" + sub + "/resourceGroups/rg/providers/Microsoft.Network/networkSecurityGroups/nsg-1",
		Type:       string(kgtypes.NodeCloudResource),
		SymbolName: "nsg-1",
		Content: `{"properties":{"securityRules":[{"properties":{"access":"Allow",` +
			`"direction":"Inbound","protocol":"Tcp","destinationPortRange":"443",` +
			`"sourceAddressPrefix":"10.0.0.0/8"}}]}}`,
	}
	kgtypes.SetValue(nsg, "resource_type", "Microsoft.Network/networkSecurityGroups")

	fc := &fakeGraphCaller{nodesByAccount: map[string][]*knowledgev1.Node{
		sub: {nsg},
	}}

	require.NoError(t, resolveNSGRules(context.Background(), fc, sub))

	tgts := fc.createBatchTargets()
	require.NotEmpty(t, tgts, "expected at least one create_batch mutation (CIDR sentinel + edge)")
	for _, tgt := range tgts {
		assert.Equal(t, "cloud", tgt.GetGraph())
		assert.Equal(t, sub, tgt.GetAccount(), "NSG edges must route by Account==%s (NOT Name)", sub)
		assert.Empty(t, tgt.GetName(), "cloud write must NOT route by Name")
	}
}

// TestResolveAzureDNS_RewritesDanglingEdgeByAccount drives resolveDNSRecordTargets:
// a DNS record with an outgoing ROUTES_TO edge to a raw IP that matches an LB
// frontend IP must be unlinked and relinked to the resolved resource ID, with the
// new edge write routed by Target.Account.
func TestResolveAzureDNS_RewritesDanglingEdgeByAccount(t *testing.T) {
	const sub = "00000000-0000-0000-0000-000000000002"
	const ip = "10.1.2.3"
	lbID := "/subscriptions/" + sub + "/resourceGroups/rg/providers/Microsoft.Network/loadBalancers/lb-1"
	recID := "/subscriptions/" + sub + "/resourceGroups/rg/providers/Microsoft.Network/dnsZones/z/recordSets/a/www"

	lb := &knowledgev1.Node{
		Id:         lbID,
		Type:       string(kgtypes.NodeCloudResource),
		SymbolName: "lb-1",
		Content:    `{"properties":{"frontendIPConfigurations":[{"properties":{"privateIPAddress":"` + ip + `"}}]}}`,
	}
	kgtypes.SetValue(lb, "resource_type", "Microsoft.Network/loadBalancers")
	rec := &knowledgev1.Node{Id: recID, Type: string(kgtypes.NodeCloudResource), SymbolName: "www"}
	kgtypes.SetValue(rec, "resource_type", "Microsoft.Network/dnsZones/recordSets")

	fc := &fakeGraphCaller{
		nodesByAccount: map[string][]*knowledgev1.Node{
			sub: {lb, rec},
		},
		outEdges: map[string][]knowledgev1.Edge{
			recID: {{FromId: recID, ToId: ip, Type: string(kgtypes.EdgeRoutesTo)}},
		},
	}

	require.NoError(t, resolveDNSRecordTargets(context.Background(), fc, sub))

	// The new (resolved) edge must have been written via create_batch to the
	// per-subscription cloud graph.
	tgts := fc.createBatchTargets()
	require.NotEmpty(t, tgts, "expected a create_batch for the rewritten ROUTES_TO edge")
	for _, tgt := range tgts {
		assert.Equal(t, "cloud", tgt.GetGraph())
		assert.Equal(t, sub, tgt.GetAccount(), "rewritten DNS edge must route by Account==%s", sub)
		assert.Empty(t, tgt.GetName())
	}

	// An unlink mutation for the dangling edge must also have been issued.
	var sawUnlink bool
	for _, req := range fc.mutations {
		if req.GetMutation().GetKind() == knowledgev1.MutationPlan_MUTATION_KIND_UNLINK {
			sawUnlink = true
			assert.Equal(t, sub, req.GetTarget().GetAccount(), "unlink must route by Account")
		}
	}
	assert.True(t, sawUnlink, "the dangling raw-IP ROUTES_TO edge must be unlinked")
}
