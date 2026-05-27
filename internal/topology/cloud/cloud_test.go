// SPDX-License-Identifier: Apache-2.0

package cloud

import (
	"context"
	"maps"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// cloud_test.go provides the shared test fixture for the cloud analyzer
// family. The analyzers read every node and edge over the wire through the
// foundation GraphCaller seam, so the fixture is a scripted Execute that
// serves a synthetic per-account graph: it answers the type-browse,
// match-all, by-id, bulk-edges, and graph-names plans the foundation
// read-helpers emit. Tests build the exact synthetic graph they need via the
// per-account AddCloudResource / AddEdge helpers, then drive an analyzer with
// a foundation.Request carrying the fixture as req.Caller.
//
// This replaces the prior store-backed cloudFixture (in-memory store.DB)
// while preserving byte-stable analyzer outputs: the analyzer ALGORITHMS are
// unchanged, only the data-access layer moved to the wire.

// fakeNode and fakeEdge are the per-account synthetic records the fixture
// stores. The fixture builds *knowledgev1.Node / knowledgev1.Edge on demand
// inside Execute, ranging by index to avoid copylocks on the proto mutex.
type fakeNode struct {
	id         string
	symbolName string
	nodeType   kgtypes.NodeType
	content    string
	metadata   map[string]string
}

type fakeEdge struct {
	from     string
	to       string
	edgeType kgtypes.EdgeType
}

// fakeAccount holds one account's synthetic nodes and edges.
type fakeAccount struct {
	nodes []fakeNode
	edges []fakeEdge
}

// cloudFixture is the scripted GraphCaller backing the cloud analyzer tests.
// It maps an account name to its synthetic graph and implements Execute by
// decoding the inbound plan shape and returning the matching carrier.
type cloudFixture struct {
	t        *testing.T
	accounts map[string]*fakeAccount
}

// newCloudFixture returns an empty fixture.
func newCloudFixture(t *testing.T) *cloudFixture {
	t.Helper()
	return &cloudFixture{t: t, accounts: map[string]*fakeAccount{}}
}

// account returns the named account's synthetic graph, creating it on first
// use so an empty account answers browses with an empty node set.
func (f *cloudFixture) account(name string) *fakeAccount {
	acct, ok := f.accounts[name]
	if !ok {
		acct = &fakeAccount{}
		f.accounts[name] = acct
	}
	return acct
}

// AddCloudResource appends one cloud-resource node to the named account.
func (f *cloudFixture) AddCloudResource(account, id, symbolName, resourceType string, meta map[string]string) {
	f.addNode(account, id, symbolName, resourceType, kgtypes.NodeCloudResource, "", meta)
}

// AddCICDResource appends one cicd-resource node to the named account.
func (f *cloudFixture) AddCICDResource(account, id, symbolName, resourceType string, meta map[string]string) {
	f.addNode(account, id, symbolName, resourceType, kgtypes.NodeCICDResource, "", meta)
}

// AddCloudResourceWithContent appends a cloud-resource node carrying Content
// (used by cert_expiry, which parses the certificate JSON out of Content).
func (f *cloudFixture) AddCloudResourceWithContent(account, id, symbolName, resourceType, content string, meta map[string]string) {
	f.addNode(account, id, symbolName, resourceType, kgtypes.NodeCloudResource, content, meta)
}

func (f *cloudFixture) addNode(account, id, symbolName, resourceType string, nodeType kgtypes.NodeType, content string, meta map[string]string) {
	md := map[string]string{}
	if resourceType != "" {
		md["resource_type"] = resourceType
	}
	maps.Copy(md, meta)
	acct := f.account(account)
	acct.nodes = append(acct.nodes, fakeNode{
		id:         id,
		symbolName: symbolName,
		nodeType:   nodeType,
		content:    content,
		metadata:   md,
	})
}

// AddEdge appends one directed edge to the named account.
func (f *cloudFixture) AddEdge(account, fromID, toID string, edgeType kgtypes.EdgeType) {
	acct := f.account(account)
	acct.edges = append(acct.edges, fakeEdge{from: fromID, to: toID, edgeType: edgeType})
}

// Execute decodes the inbound plan and serves the matching carrier. It
// recognizes: RETURN_MODE_EDGES (bulk-edges over an id set + optional type
// filter), RETURN_MODE_GRAPH_NAMES (modules), by-id node lookups, and the
// default node browse (type-filtered or match-all).
func (f *cloudFixture) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	q := req.GetQuery()
	acct := f.accountForTarget(req.GetTarget())
	resp := &knowledgev1.ExecuteResponse{}
	switch {
	case q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_EDGES:
		resp.Edges = f.edgesFor(acct, q.GetIds(), q.GetSelection().GetEdgeTypes())
	case q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_GRAPH_NAMES:
		resp.GraphNames = f.graphNames()
	case q.GetById() != "":
		resp.Nodes = f.nodeByID(acct, q.GetById())
	default:
		resp.Nodes = f.nodesFor(acct, q.GetSelection().GetNodeTypes())
	}
	return resp, nil
}

// accountForTarget resolves the GraphSelector's Account field to the synthetic
// account. A nil target (or empty account) maps to the empty account so a
// browse against an unseeded graph returns no nodes.
func (f *cloudFixture) accountForTarget(target *knowledgev1.GraphSelector) *fakeAccount {
	name := ""
	if target != nil {
		name = target.GetAccount()
	}
	return f.account(name)
}

// nodesFor returns the account's nodes filtered to nodeTypes (empty = all).
func (f *cloudFixture) nodesFor(acct *fakeAccount, nodeTypes []string) []*knowledgev1.Node {
	typeSet := map[string]bool{}
	for _, t := range nodeTypes {
		typeSet[t] = true
	}
	var out []*knowledgev1.Node
	for i := range acct.nodes {
		fn := &acct.nodes[i]
		if len(typeSet) > 0 && !typeSet[string(fn.nodeType)] {
			continue
		}
		out = append(out, buildNode(fn))
	}
	return out
}

// nodeByID returns the single node matching id, or an empty slice.
func (f *cloudFixture) nodeByID(acct *fakeAccount, id string) []*knowledgev1.Node {
	for i := range acct.nodes {
		if acct.nodes[i].id == id {
			return []*knowledgev1.Node{buildNode(&acct.nodes[i])}
		}
	}
	return nil
}

// edgesFor returns the account's edges incident to any node in ids and
// matching one of edgeTypes (empty = any). Both directions are unioned,
// matching the foundation FetchEdges node-SET both-direction semantics.
func (f *cloudFixture) edgesFor(acct *fakeAccount, ids, edgeTypes []string) []*knowledgev1.Edge {
	idSet := map[string]bool{}
	for _, id := range ids {
		idSet[id] = true
	}
	typeSet := map[string]bool{}
	for _, et := range edgeTypes {
		typeSet[et] = true
	}
	var out []*knowledgev1.Edge
	for i := range acct.edges {
		e := &acct.edges[i]
		if !idSet[e.from] && !idSet[e.to] {
			continue
		}
		if len(typeSet) > 0 && !typeSet[string(e.edgeType)] {
			continue
		}
		out = append(out, buildEdge(e))
	}
	return out
}

// graphNames returns one GraphInfo per seeded account, in a deterministic
// (sorted) order so cross-account walks are stable across runs.
func (f *cloudFixture) graphNames() []*knowledgev1.GraphInfo {
	names := make([]string, 0, len(f.accounts))
	for name := range f.accounts {
		if name == "" {
			continue
		}
		names = append(names, name)
	}
	sortStrings(names)
	out := make([]*knowledgev1.GraphInfo, 0, len(names))
	for _, name := range names {
		gi := &knowledgev1.GraphInfo{}
		gi.Name = name
		out = append(out, gi)
	}
	return out
}

// buildNode constructs a wire node from a synthetic record. Metadata is
// copied so callers cannot mutate the fixture through the returned node.
func buildNode(fn *fakeNode) *knowledgev1.Node {
	n := &knowledgev1.Node{}
	n.Id = fn.id
	n.Type = string(fn.nodeType)
	n.SymbolName = fn.symbolName
	n.Content = fn.content
	if len(fn.metadata) > 0 {
		md := make(map[string]string, len(fn.metadata))
		maps.Copy(md, fn.metadata)
		n.Metadata = md
	}
	return n
}

// buildEdge constructs a wire edge from a synthetic record.
func buildEdge(e *fakeEdge) *knowledgev1.Edge {
	out := &knowledgev1.Edge{}
	out.FromId = e.from
	out.ToId = e.to
	out.Type = string(e.edgeType)
	return out
}

// sortStrings is a tiny insertion sort kept local so the fixture has no
// import beyond the wire vocabulary and the standard testing package.
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}

// cloudReq builds a foundation.Request for the cloud graph backed by the
// fixture, for the given account.
func (f *cloudFixture) cloudReq(account string, topK int) foundation.Request {
	return foundation.Request{
		Caller: f,
		Graph:  kgtypes.GraphCloud,
		Name:   account,
		TopK:   topK,
	}
}

// orphanGraphFor builds the in-memory orphanGraph for the named account from
// the fixture's synthetic nodes + edges. The orphan rule unit tests call a
// rule directly with this graph, mirroring the prior tests that passed a
// scoped store.DB. Note: the rules read edge presence by node ID, so building
// the graph from the account's full node set reproduces the prior per-account
// scoped view exactly.
func (f *cloudFixture) orphanGraphFor(t *testing.T, account string) *orphanGraph {
	t.Helper()
	acct := f.account(account)
	nodeByID := make(map[string]*knowledgev1.Node, len(acct.nodes))
	for i := range acct.nodes {
		n := buildNode(&acct.nodes[i])
		nodeByID[n.Id] = n
	}
	edges := make([]knowledgev1.Edge, 0, len(acct.edges))
	for i := range acct.edges {
		edges = append(edges, *buildEdge(&acct.edges[i]))
	}
	return &orphanGraph{edges: newEdgeIndex(edges), nodeByID: nodeByID}
}

// nodeFor returns the wire node with the given ID in the named account. Fails
// the test if the node is absent (the rule tests always add it first).
func (f *cloudFixture) nodeFor(t *testing.T, account, id string) *knowledgev1.Node {
	t.Helper()
	acct := f.account(account)
	for i := range acct.nodes {
		if acct.nodes[i].id == id {
			return buildNode(&acct.nodes[i])
		}
	}
	t.Fatalf("node %q not found in account %q", id, account)
	return nil
}

// compile-time assertion: the fixture satisfies the wire seam.
var _ foundation.GraphCaller = (*cloudFixture)(nil)
