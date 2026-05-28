// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/enginetest"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// fakeGraphCaller is a package-local postpopulate.GraphCaller for the GCP
// PostPopulate family. It resolves cloud reads by Target.Account (miss on empty),
// serves outgoing/incoming edge reads, and records create_batch / unlink
// mutations so a test can assert writes route by Account (NOT Name). Replaces the
// real store.DB — no store.Init / store.Store().
type fakeGraphCaller struct {
	nodesByProject map[string][]*knowledgev1.Node
	outEdges       map[string][]knowledgev1.Edge // keyed by FromID
	inEdges        map[string][]knowledgev1.Edge // keyed by ToID

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
		for p := range f.nodesByProject {
			infos = append(infos, &knowledgev1.GraphInfo{Name: p})
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
	for i := range set[ids[0]] {
		e := &set[ids[0]][i]
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
	for _, n := range f.nodesByProject[acct] {
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

// TestResolveSharedVPCEdges_RoutesLocalAndHostByAccount drives the real
// resolveSharedVPCEdges through the wire fake: a service-project subnet that
// uses a host-project network must emit a SHARED_WITH edge into BOTH the local
// (service) project graph and the host project graph, each routed by
// Target.Account (NOT Name).
func TestResolveSharedVPCEdges_RoutesLocalAndHostByAccount(t *testing.T) {
	const svcProject = "service-proj"
	const hostProject = "host-proj"

	subnetID := "https://www.googleapis.com/compute/v1/projects/" + svcProject + "/regions/us/subnetworks/sn-1"
	networkLink := "https://www.googleapis.com/compute/v1/projects/" + hostProject + "/global/networks/shared-vpc"
	subnet := &knowledgev1.Node{
		Id:         subnetID,
		Type:       string(kgtypes.NodeCloudResource),
		SymbolName: "sn-1",
		Content:    `{"network":"` + networkLink + `"}`,
	}
	kgtypes.SetValue(subnet, "resource_type", "gcp:compute:subnetwork")

	fc := &fakeGraphCaller{nodesByProject: map[string][]*knowledgev1.Node{
		svcProject:  {subnet},
		hostProject: {},
	}}

	require.NoError(t, resolveSharedVPCEdges(context.Background(), fc, svcProject))

	seen := map[string]bool{}
	for _, tgt := range fc.createBatchTargets() {
		assert.Equal(t, "cloud", tgt.GetGraph())
		assert.Empty(t, tgt.GetName(), "shared-vpc write must NOT route by Name")
		seen[tgt.GetAccount()] = true
	}
	assert.True(t, seen[svcProject], "SHARED_WITH edge must land in the service project graph")
	assert.True(t, seen[hostProject], "SHARED_WITH edge must land in the host project graph")
}

// TestResolveFirewallRules_RoutesByAccount asserts firewall reachability edges
// (+ CIDR sentinel nodes) land in the per-project cloud graph by Account.
func TestResolveFirewallRules_RoutesByAccount(t *testing.T) {
	const project = "fw-proj"
	fw := &knowledgev1.Node{
		Id:         "projects/" + project + "/global/firewalls/allow-https",
		Type:       string(kgtypes.NodeCloudResource),
		SymbolName: "allow-https",
		Content: `{"direction":"INGRESS","sourceRanges":["10.0.0.0/8"],` +
			`"allowed":[{"IPProtocol":"tcp","ports":["443"]}],"targetTags":["web"]}`,
	}
	kgtypes.SetValue(fw, "resource_type", "gcp:compute:firewall")
	inst := &knowledgev1.Node{
		Id:         "projects/" + project + "/zones/us/instances/web-1",
		Type:       string(kgtypes.NodeCloudResource),
		SymbolName: "web-1",
		Content:    `{"tags":{"items":["web"]}}`,
	}
	kgtypes.SetValue(inst, "resource_type", "gcp:compute:instance")

	fc := &fakeGraphCaller{nodesByProject: map[string][]*knowledgev1.Node{project: {fw, inst}}}

	require.NoError(t, resolveFirewallRules(context.Background(), fc, project))

	tgts := fc.createBatchTargets()
	require.NotEmpty(t, tgts, "expected at least one create_batch mutation")
	for _, tgt := range tgts {
		assert.Equal(t, "cloud", tgt.GetGraph())
		assert.Equal(t, project, tgt.GetAccount(), "firewall edges must route by Account==%s", project)
		assert.Empty(t, tgt.GetName(), "cloud write must NOT route by Name")
	}
}
