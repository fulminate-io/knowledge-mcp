// SPDX-License-Identifier: Apache-2.0

package exposure

import (
	"context"
	"maps"
	"sort"
	"testing"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

// exposure_test.go provides the shared scripted GraphCaller fixture for the
// cross-graph exposure analyzer family. The analyzers read every node and
// edge over the wire through the foundation.GraphCaller seam, so the fixture
// is a scripted Execute that serves a synthetic per-(graph, account) graph: it
// answers the type-browse, match-all, by-id, bulk-edges, graph-names, and the
// meta-filtered knowledge-findings plans the foundation read-helpers emit.
//
// It replaces the prior store-backed cloudFixture (an in-memory store.DB
// rooted at t.TempDir()) while preserving byte-stable analyzer outputs: the
// analyzer ALGORITHMS are unchanged, only the data-access layer moved to the
// wire. The fixture mirrors the old cloudFixture's per-account builder API
// (AddCloudResource / AddEdge / Account) so the exposure-family fixture
// helpers (iam_fixture_test.go, public_exposure_fixture_test.go, …) keep their
// shapes, and adds:
//
//   - a multi-account graph-names answer (FetchGraphNames) for the
//     cross-account IAM escalation walk;
//   - a knowledge-graph finding seed (AddKnowledgeFinding) for the
//     public_exposure sensitive-role escalation lookup
//     (FetchKnowledgeFindings).

// The canonical cloud account names accountA / accountB are defined in
// iam_rules_test.go (where the cross-account IAM tests assert against them);
// the harness references them but does not redeclare them.

// fakeNode and fakeEdge are the per-account synthetic records the fixture
// stores. The fixture builds *knowledgev1.Node / *knowledgev1.Edge on demand
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
	evidence string
}

// fakeAccount holds one (graph, account) instance's synthetic nodes/edges.
type fakeAccount struct {
	nodes []fakeNode
	edges []fakeEdge
}

// cloudFixture is the scripted GraphCaller backing the exposure analyzer
// tests. accounts is keyed by cloud-account name; knowledge holds the
// knowledge-graph findings the sensitive-terminal classifier consults; linkage
// holds the linkage-graph proxy nodes + bridge edges the unified walker
// follows. The zero account name "" maps to an empty account so a browse
// against an unseeded graph returns no nodes.
type cloudFixture struct {
	t         *testing.T
	accounts  map[string]*fakeAccount
	knowledge *fakeAccount
	linkage   *fakeAccount
	// empty is what the READ path resolves an unseeded account name to. It is
	// shared and never written; see lookupAccount.
	empty *fakeAccount
}

// newCloudFixture returns an empty fixture.
func newCloudFixture(t *testing.T) *cloudFixture {
	t.Helper()
	return &cloudFixture{
		t:         t,
		accounts:  map[string]*fakeAccount{},
		knowledge: &fakeAccount{},
		linkage:   &fakeAccount{},
		empty:     &fakeAccount{},
	}
}

// account returns the named cloud account's synthetic graph, creating it on
// first use.
//
// THIS IS THE SEEDING ACCESSOR AND IT MUTATES. Only the Add* helpers may call
// it, and only from the test body before the analyzer runs. The query path uses
// lookupAccount instead — see the note there.
func (f *cloudFixture) account(name string) *fakeAccount {
	acct, ok := f.accounts[name]
	if !ok {
		acct = &fakeAccount{}
		f.accounts[name] = acct
	}
	return acct
}

// lookupAccount is the READ-ONLY account accessor, and the split from account()
// is load-bearing rather than tidiness.
//
// Execute serves the analyzer's rule fanout, which runs its rules on concurrent
// goroutines. account() creates on first use, and a map create is a WRITE — so
// resolving an unseeded account name on that path raced two goroutines against
// the same map and the race detector caught it: a write in account() from one
// rule against a read in account() from another. The race was the FIXTURE's, not
// the analyzer's; the analyzer only ever reads through this seam.
//
// Concurrent map READS are safe, so removing the write is the whole fix and no
// lock is needed on the query path. An unseeded name resolves to a shared empty
// account, which is exactly what the lazily-created one used to be — a browse
// against an unseeded graph returns no nodes either way.
func (f *cloudFixture) lookupAccount(name string) *fakeAccount {
	if acct, ok := f.accounts[name]; ok {
		return acct
	}
	return f.empty
}

// AddCloudResource appends one cloud-resource node to the named account and
// returns the synthetic record's wire view. The returned node mirrors what the
// old store-backed fixture returned (callers occasionally pass it straight to a
// rule).
func (f *cloudFixture) AddCloudResource(account, id, symbolName, resourceType string, meta map[string]string) *knowledgev1.Node {
	return f.addNode(f.account(account), id, symbolName, resourceType, kgtypes.NodeCloudResource, "", meta)
}

// AddCloudResourceWithContent appends a cloud-resource node carrying Content
// (used by seed/IAM rules that parse JSON out of Content).
func (f *cloudFixture) AddCloudResourceWithContent(account, id, symbolName, resourceType, content string, meta map[string]string) *knowledgev1.Node {
	return f.addNode(f.account(account), id, symbolName, resourceType, kgtypes.NodeCloudResource, content, meta)
}

func (f *cloudFixture) addNode(acct *fakeAccount, id, symbolName, resourceType string, nodeType kgtypes.NodeType, content string, meta map[string]string) *knowledgev1.Node {
	md := map[string]string{}
	if resourceType != "" {
		md["resource_type"] = resourceType
	}
	maps.Copy(md, meta)
	acct.nodes = append(acct.nodes, fakeNode{
		id:         id,
		symbolName: symbolName,
		nodeType:   nodeType,
		content:    content,
		metadata:   md,
	})
	return buildNode(&acct.nodes[len(acct.nodes)-1])
}

// setNodeContent overwrites the Content of an existing cloud-resource node.
func (f *cloudFixture) setNodeContent(account, id, content string) {
	acct := f.account(account)
	for i := range acct.nodes {
		if acct.nodes[i].id == id {
			acct.nodes[i].content = content
			return
		}
	}
}

// setNodeMeta sets/overwrites a metadata key on an existing cloud-resource
// node (mirrors the old SetValue + Upsert pattern the inline-policy helper
// used).
func (f *cloudFixture) setNodeMeta(account, id, key, value string) {
	acct := f.account(account)
	for i := range acct.nodes {
		if acct.nodes[i].id == id {
			if acct.nodes[i].metadata == nil {
				acct.nodes[i].metadata = map[string]string{}
			}
			acct.nodes[i].metadata[key] = value
			return
		}
	}
}

// AddEdge appends one directed edge to the named cloud account.
func (f *cloudFixture) AddEdge(account, fromID, toID string, edgeType kgtypes.EdgeType) {
	acct := f.account(account)
	acct.edges = append(acct.edges, fakeEdge{from: fromID, to: toID, edgeType: edgeType})
}

// AddEdgeWithEvidence appends one directed edge carrying the given Evidence
// string. The reachability analyzers decode protocol/port/CIDR/is_nacl out of
// the edge Evidence, so the SG / NACL / ANP fixture helpers route through here
// (mirroring the collector's LinkBatch([]Edge{{…Evidence…}}) writes).
func (f *cloudFixture) AddEdgeWithEvidence(account, fromID, toID string, edgeType kgtypes.EdgeType, evidence string) {
	acct := f.account(account)
	acct.edges = append(acct.edges, fakeEdge{from: fromID, to: toID, edgeType: edgeType, evidence: evidence})
}

// AddKnowledgeFinding appends a topology finding to the knowledge graph with
// the algorithm + primary_evidence metadata the sensitive-terminal classifier
// matches on via FetchKnowledgeFindings.
func (f *cloudFixture) AddKnowledgeFinding(id, algorithm, primaryEvidence string) {
	f.addNode(f.knowledge, id, id, "", kgtypes.NodeFinding, "", map[string]string{
		"algorithm":        algorithm,
		"primary_evidence": primaryEvidence,
	})
}

// AddLinkageNode appends a proxy node to the linkage graph (carrying its
// foreign_id metadata) so the unified walker's bridge resolution can read it
// back.
func (f *cloudFixture) AddLinkageNode(id, foreignID string, meta map[string]string) {
	md := map[string]string{}
	if foreignID != "" {
		md["foreign_id"] = foreignID
	}
	maps.Copy(md, meta)
	f.addNode(f.linkage, id, id, "", kgtypes.NodeProxy, "", md)
}

// AddLinkageEdge appends a directed edge to the linkage graph.
func (f *cloudFixture) AddLinkageEdge(fromID, toID string, edgeType kgtypes.EdgeType) {
	f.linkage.edges = append(f.linkage.edges, fakeEdge{from: fromID, to: toID, edgeType: edgeType})
}

// Execute decodes the inbound plan and serves the matching carrier. It routes
// by graph type (cloud account, knowledge, or linkage) off the envelope
// Target, then by plan shape (edges / graph-names / by-id / node-browse).
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
		resp.Nodes = nodeByID(acct, q.GetById())
	default:
		resp.Nodes = keysetPage(
			f.nodesFor(acct, q.GetSelection().GetNodeType(), q.GetSelection().GetNodeTypes(), q.GetSelection().GetMetadataPredicates()),
			q.AfterId, int(q.GetLimit()))
	}
	return resp, nil
}

// keysetPage applies the server's browse paging contract to an already-filtered
// node set: ids strictly AFTER the cursor, ascending, capped at limit. Only
// applied when after_id is PRESENT, so the un-cursored browses keep serving in
// seeded order.
//
// Honoring the cursor is not cosmetic fidelity here. A caller that DRAINS pages
// terminates on the first short page, so a cursor-blind fake that re-serves the
// whole set forever never terminates once the fixture holds at least a page of
// nodes — a hang rather than a failed assertion.
func keysetPage(nodes []*knowledgev1.Node, afterID *string, limit int) []*knowledgev1.Node {
	if afterID != nil {
		sort.Slice(nodes, func(i, j int) bool { return nodes[i].GetId() < nodes[j].GetId() })
		if cursor := *afterID; cursor != "" {
			kept := nodes[:0]
			for _, n := range nodes {
				if n.GetId() > cursor {
					kept = append(kept, n)
				}
			}
			nodes = kept
		}
	}
	if limit > 0 && len(nodes) > limit {
		nodes = nodes[:limit]
	}
	return nodes
}

// accountForTarget resolves the envelope GraphSelector to the synthetic
// account/graph. graph=knowledge → the knowledge bucket; graph=linkage → the
// linkage bucket; otherwise the named cloud account (empty → empty account).
func (f *cloudFixture) accountForTarget(target *knowledgev1.GraphSelector) *fakeAccount {
	if target == nil {
		return f.lookupAccount("")
	}
	switch kgtypes.GraphType(target.GetGraph()) {
	case kgtypes.GraphKnowledge:
		return f.knowledge
	case kgtypes.GraphLinkage:
		return f.linkage
	default:
		return f.lookupAccount(target.GetAccount())
	}
}

// nodesFor returns the account's nodes filtered to the requested type(s) and
// metadata predicates. nodeType is the singular type-browse selector
// (FetchKnowledgeFindings); nodeTypes is the plural selector
// (FetchNodesByType); preds gates by metadata equality (the knowledge-findings
// algorithm + primary_evidence filter).
func (f *cloudFixture) nodesFor(acct *fakeAccount, nodeType string, nodeTypes []string, preds []*knowledgev1.MetadataPredicate) []*knowledgev1.Node {
	typeSet := map[string]bool{}
	for _, t := range nodeTypes {
		typeSet[t] = true
	}
	if nodeType != "" {
		typeSet[nodeType] = true
	}
	var out []*knowledgev1.Node
	for i := range acct.nodes {
		fn := &acct.nodes[i]
		if len(typeSet) > 0 && !typeSet[string(fn.nodeType)] {
			continue
		}
		if !metadataMatches(fn, preds) {
			continue
		}
		out = append(out, buildNode(fn))
	}
	return out
}

// metadataMatches reports whether the node satisfies every metadata equality
// predicate. An empty predicate set matches everything.
func metadataMatches(fn *fakeNode, preds []*knowledgev1.MetadataPredicate) bool {
	for _, p := range preds {
		if fn.metadata[p.GetKey()] != p.GetValue() {
			return false
		}
	}
	return true
}

// nodeByID returns the single node matching id, or an empty slice.
func nodeByID(acct *fakeAccount, id string) []*knowledgev1.Node {
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

// graphNames returns one GraphInfo per seeded cloud account, sorted, so
// cross-account walks are deterministic.
func (f *cloudFixture) graphNames() []*knowledgev1.GraphInfo {
	names := make([]string, 0, len(f.accounts))
	for name := range f.accounts {
		if name == "" {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]*knowledgev1.GraphInfo, 0, len(names))
	for _, name := range names {
		gi := &knowledgev1.GraphInfo{}
		gi.Name = name
		out = append(out, gi)
	}
	return out
}

// buildNode constructs a wire node from a synthetic record. Metadata is copied
// so callers cannot mutate the fixture through the returned node.
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
	out.Evidence = e.evidence
	return out
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

// reader returns a cloudReader bound to this fixture for the given account —
// the unit of access the rule/index helpers consume directly.
func (f *cloudFixture) reader(account string) *cloudReader {
	return newCloudReader(f, account)
}

// compile-time assertion: the fixture satisfies the wire seam.
var _ foundation.GraphCaller = (*cloudFixture)(nil)

// newTestCtx returns a plain context for the wire-backed tests. The prior
// store-backed fixture installed a write txn here; the scripted GraphCaller
// has no store, so a background context is all the relocated test bodies need.
// Kept as a named helper so the relocated test bodies keep their newTestCtx(t)
// call shape unchanged.
func newTestCtx(_ testing.TB) context.Context {
	return context.Background()
}
