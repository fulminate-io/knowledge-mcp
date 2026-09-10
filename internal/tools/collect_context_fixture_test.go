// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/externalcollector"
)

// collect_context_fixture_test.go — the READ SEAM the context fill pulls
// through, as a fake.
//
// THERE IS ONE SEAM NOW WHERE THERE WERE TWO. The fill used to read the cloud
// family through the ingest service's subgraph fetch and the code family through
// the generic Execute carrier, and each had its own fake. The subgraph RPC and
// the cloud family are gone; every family — code and every registered type — is
// read through the one carrier, so one fake answers the whole contract and a
// second could no longer disagree with it.
//
// THE PACKAGE'S EXISTING DEPS DOUBLE SUPPLIES NEITHER READ: customDeps returns
// nil from GraphCaller. That nil is correct for every test that predates this
// contract and is exactly what the fill's own refusal rows exercise; the rows
// that need a filled block wire one of these instead.
//
// THE FAKE ANSWERS IN THE PRODUCTION SHAPE rather than a convenient one: real
// ExecuteRequests decoded through the same engine decoders the linker's own read
// primitives use, honoring the keyset cursor and the pivot id set.

// contextGraphCaller answers the three Execute shapes the fill issues: the
// query(mode:"modules") graph-name lookup, the singular type browse the node
// drain pages through, and the pivoted edge read. Anything else is an error
// naming what it received, so a fill that issued an unexpected read fails
// loudly rather than reading an empty answer.
type contextGraphCaller struct {
	// graphs maps a graph name to its nodes, per graph TYPE. The outer key is
	// the graph type because the fill is now family-parameterized: a fixture that
	// keyed on name alone could not tell a code graph named "api" from a
	// registered aws graph named "api", which is exactly the confusion the
	// instance-key routing exists to prevent.
	graphs map[string]map[string][]*knowledgev1.Node
	// edges maps a graph type to that type's edges, keyed by graph name.
	edges map[string]map[string][]knowledgev1.Edge
	// names is the ordered graph-name answer per graph type. It is separate from
	// graphs so a fixture can name a graph that holds no nodes.
	names map[string][]string

	// err, when set, fails EVERY read. It is what distinguishes a read that
	// errored from a store that held nothing — two facts a module cannot tell
	// apart from the block it receives.
	err error

	mu        sync.Mutex
	browsed   []string
	edgePivot []string
	nameCall  int
}

func (f *contextGraphCaller) Execute(
	_ context.Context, req *knowledgev1.ExecuteRequest,
) (*knowledgev1.ExecuteResponse, error) {
	if f.err != nil {
		return nil, f.err
	}
	q := req.GetQuery()
	graphType := req.GetTarget().GetGraph()
	switch q.GetReturnMode() {
	case knowledgev1.ReturnMode_RETURN_MODE_GRAPH_NAMES:
		f.mu.Lock()
		f.nameCall++
		f.mu.Unlock()
		return f.graphNamesResponse(graphType)
	case knowledgev1.ReturnMode_RETURN_MODE_EDGES:
		return f.edgesResponse(graphType, req, q)
	}
	// A BROWSE WITH NO TYPE KEY IS THE all_node_types DRAIN, and it is admitted
	// rather than refused: the selector's whole point is a read with no type key
	// at all, so a fixture that required one could not observe it. It is recorded
	// under a distinct label so a row can tell the two reads apart.
	nodeType := q.GetSelection().GetNodeType()
	name := instanceNameOf(req.GetTarget())
	label := nodeType
	if nodeType == "" {
		label = "*"
	}
	f.mu.Lock()
	f.browsed = append(f.browsed, graphType+"/"+name+"/"+label)
	f.mu.Unlock()
	return f.browseResponse(graphType, name, nodeType, q), nil
}

// instanceNameOf reads whichever instance field the selector carries. THE FAKE
// MUST NOT PICK ONE: a code graph is addressed by Repo and a registered custom
// graph by Name, and a fixture that only read Repo would answer empty for every
// registered type while looking like a graph that held nothing.
func instanceNameOf(sel *knowledgev1.GraphSelector) string {
	if r := sel.GetRepo(); r != "" {
		return r
	}
	if a := sel.GetAccount(); a != "" {
		return a
	}
	return sel.GetName()
}

// graphNamesResponse answers the modules query in the carrier the engine's own
// decoder reads.
func (f *contextGraphCaller) graphNamesResponse(graphType string) (*knowledgev1.ExecuteResponse, error) {
	names := f.names[graphType]
	infos := make([]*knowledgev1.GraphInfo, 0, len(names))
	for _, n := range names {
		infos = append(infos, &knowledgev1.GraphInfo{Name: n})
	}
	return &knowledgev1.ExecuteResponse{GraphNames: infos}, nil
}

// browseResponse answers ONE KEYSET PAGE of a type browse. It honors after_id
// and limit rather than returning everything, so the drain's paging is actually
// exercised: a fake that ignored the cursor would let a caller that never paged
// pass.
func (f *contextGraphCaller) browseResponse(
	graphType, name, nodeType string, q *knowledgev1.QueryPlan,
) *knowledgev1.ExecuteResponse {
	after := q.GetAfterId()
	limit := int(q.GetLimit())
	var page []*knowledgev1.Node
	for _, n := range f.graphs[graphType][name] {
		// An EMPTY nodeType is the typeless drain: every node of the graph, which
		// is what the store's own browse returns when the payload carries no type
		// key.
		if n.Id <= after || (nodeType != "" && n.Type != nodeType) {
			continue
		}
		page = append(page, n)
		if limit > 0 && len(page) == limit {
			break
		}
	}
	return &knowledgev1.ExecuteResponse{Nodes: page}
}

// edgesResponse answers a pivoted edge read: every edge incident to one of the
// requested ids, in either direction.
//
// IT HONORS THE PIVOT SET rather than returning the graph's whole edge list,
// which is what makes the pivot assertions mean anything: a fake that ignored
// req.Ids would let a fill that sent no pivots — the unbounded read the pivot
// paging exists to retire — pass unnoticed.
func (f *contextGraphCaller) edgesResponse(
	graphType string, req *knowledgev1.ExecuteRequest, q *knowledgev1.QueryPlan,
) (*knowledgev1.ExecuteResponse, error) {
	name := instanceNameOf(req.GetTarget())
	want := make(map[string]bool, len(q.GetIds()))
	for _, id := range q.GetIds() {
		want[id] = true
	}
	f.mu.Lock()
	f.edgePivot = append(f.edgePivot, q.GetIds()...)
	f.mu.Unlock()
	// INDEXED, NOT RANGED BY VALUE: knowledgev1.Edge embeds a protoimpl
	// MessageState carrying a mutex, so a range variable would copy a lock.
	var out []*knowledgev1.Edge
	seeded := f.edges[graphType][name]
	for i := range seeded {
		e := &seeded[i]
		if !want[e.GetFromId()] && !want[e.GetToId()] {
			continue
		}
		out = append(out, &knowledgev1.Edge{FromId: e.GetFromId(), ToId: e.GetToId(), Type: e.GetType()})
	}
	return &knowledgev1.ExecuteResponse{Edges: out}, nil
}

func (f *contextGraphCaller) browseCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.browsed)
}

func (f *contextGraphCaller) nameCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.nameCall
}

func (f *contextGraphCaller) edgePivots() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.edgePivot...)
}

// codeFileNode builds one code-graph file node in the shape a type browse
// returns.
func codeFileNode(id, path, body string) *knowledgev1.Node {
	return &knowledgev1.Node{Id: id, Type: "file", FilePath: path, Content: body, SymbolName: path}
}

// resourceNode builds one registered-graph resource node in the shape a type
// browse returns: whole node bodies, every field the server had, metadata
// included, so the projection has something to leave behind.
func resourceNode(id, symbol string, metadata map[string]string) *knowledgev1.Node {
	return &knowledgev1.Node{
		Id:          id,
		Type:        "aws-resource",
		SymbolName:  symbol,
		Description: "described " + id,
		Content:     "content of " + id,
		Metadata:    metadata,
	}
}

// chartFileCaller is the standing code fixture: two repositories, each holding a
// Chart.yaml and an unrelated file, so the basename narrowing has something to
// exclude and the graph-name arm has more than one answer.
func chartFileCaller() *contextGraphCaller {
	return &contextGraphCaller{
		names: map[string][]string{"code": {"api", "web"}},
		graphs: map[string]map[string][]*knowledgev1.Node{"code": {
			"api": {
				codeFileNode("api:chart", "deploy/Chart.yaml", "apiVersion: v2\nname: api-chart\n"),
				codeFileNode("api:main", "cmd/main.go", "package main"),
			},
			"web": {
				codeFileNode("web:chart", "helm/Chart.yaml", "apiVersion: v2\nname: web-chart\n"),
				codeFileNode("web:index", "index.html", "<html></html>"),
			},
		}},
	}
}

// twoResourceCaller is the standing REGISTERED-TYPE fixture: one aws graph, two
// resource nodes, one dependency edge between them. The second node holds a
// clientId the first does not, which is the control for the omitted-key row.
func twoResourceCaller() *contextGraphCaller {
	return &contextGraphCaller{
		names: map[string][]string{"aws": {"prod"}},
		graphs: map[string]map[string][]*knowledgev1.Node{"aws": {
			"prod": {
				resourceNode("i-1", "api-server", map[string]string{"resource_type": "ec2:instance"}),
				resourceNode("i-2", "api-db", map[string]string{"resource_type": "ec2:instance", "clientId": "abc-123"}),
			},
		}},
		edges: map[string]map[string][]knowledgev1.Edge{"aws": {
			"prod": {{FromId: "i-1", ToId: "i-2", Type: "DEPENDS_ON"}},
		}},
	}
}

// bulkResourceCaller builds a graph of n nodes each carrying a body of
// contentLen bytes, for the unbounded-argument row. The bytes are real: a
// fixture that declared a big size and sent a small document would prove
// nothing.
func bulkResourceCaller(n, contentLen int) *contextGraphCaller {
	nodes := make([]*knowledgev1.Node, 0, n)
	body := strings.Repeat("x", contentLen)
	for i := range n {
		nodes = append(nodes, &knowledgev1.Node{
			// FIVE DIGITS, NOT THREE. The drain's cursor is an id-keyset compare, so
			// the ids must sort lexicographically in the same order they were
			// seeded; at three digits "i-1000" sorts BEFORE "i-999" and the page
			// after the first thousand comes back empty, which reads as a truncated
			// block rather than as a fixture that cannot count.
			Id:      fmt.Sprintf("i-%05d", i),
			Type:    "aws-resource",
			Content: body,
		})
	}
	return &contextGraphCaller{
		names:  map[string][]string{"aws": {"prod"}},
		graphs: map[string]map[string][]*knowledgev1.Node{"aws": {"prod": nodes}},
	}
}

// --- the module's side of the wire ---

// registeredCRUD is a graph-type registry holding exactly the named types. IT IS
// REQUIRED FOR EVERY NON-code FAMILY: the fill reads the registry to decide
// whether a declared family can be supplied, so a fixture that left the CRUD
// seam nil would be asserting the unreadable-registry refusal rather than the
// behavior under test.
func registeredCRUD(names ...string) *stubGraphTypeCRUD {
	defs := make([]*knowledgev1.GraphTypeDef, 0, len(names))
	for _, n := range names {
		defs = append(defs, &knowledgev1.GraphTypeDef{Name: n})
	}
	return &stubGraphTypeCRUD{defs: defs}
}

// browsedReads returns the node-browse reads this caller answered, as
// `<graph type>/<name>/<node type>` with `*` for the typeless drain the
// all_node_types selector issues.
func (f *contextGraphCaller) browsedReads() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.browsed...)
}

// decodeContextDeclaration decodes a declaration from its JSON form, which is
// how an operator's file carries it — so a row about a refusal at LOAD time
// exercises the same decode the loader does rather than a hand-built struct that
// cannot express a key-order question.
func decodeContextDeclaration(t *testing.T, raw string) externalcollector.ContextDeclaration {
	t.Helper()
	var decl externalcollector.ContextDeclaration
	require.NoError(t, json.Unmarshal([]byte(raw), &decl))
	return decl
}
