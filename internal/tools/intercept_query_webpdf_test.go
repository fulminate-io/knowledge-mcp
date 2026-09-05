// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// rawGraphFake is a purpose-built GraphCaller for the raw-graph arms. The shared
// fakeGraphCaller answers a Match scan only when the plan carries a NodeType, and
// these arms read by id, so that fake would return nothing here and the tests
// would assert against an empty corpus.
//
// IT ANSWERS EACH READ FOR WHAT IT ASKED. The segment-backed arm issues FOUR
// reads with three different shapes — the collected-graph catalog, the bulk hit
// hydrate, the CONTAINS pivot, and the bulk parent hydrate — and a fake that
// served one canned node set to all of them would hand the parent section back on
// the HIT hydrate, resolving the heading whether or not the parent hydrate exists.
// The ids[] arm therefore returns ONLY the ids it was given.
type rawGraphFake struct {
	nodes []*knowledgev1.Node
	edges []*knowledgev1.Edge
	stats *knowledgev1.GraphStats
	// graphNames is the COLLECTED catalog the arm's existence gate reads. Nil is
	// the never-collected state, so a fixture meaning "this graph exists" says so.
	graphNames []string
	nodeReads  int
	edgeReads  int
	nameReads  int
	statsReads int
	targets    []*knowledgev1.GraphSelector
	// idTargets records the target of each ids[] NODE read alone. The catalog read
	// runs FIRST and carries no instance name, so targets[0] no longer witnesses
	// which graph the ranked read actually addressed.
	idTargets []*knowledgev1.GraphSelector
}

func (f *rawGraphFake) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	f.targets = append(f.targets, req.GetTarget())
	q := req.GetQuery()
	switch q.GetReturnMode() {
	case knowledgev1.ReturnMode_RETURN_MODE_GRAPH_NAMES:
		f.nameReads++
		infos := make([]*knowledgev1.GraphInfo, 0, len(f.graphNames))
		for _, n := range f.graphNames {
			infos = append(infos, &knowledgev1.GraphInfo{Name: n})
		}
		return &knowledgev1.ExecuteResponse{GraphNames: infos}, nil
	case knowledgev1.ReturnMode_RETURN_MODE_EDGES:
		f.edgeReads++
		return &knowledgev1.ExecuteResponse{Edges: f.edges}, nil
	default:
		f.nodeReads++
		f.idTargets = append(f.idTargets, req.GetTarget())
		want := map[string]bool{}
		for _, id := range q.GetIds() {
			want[id] = true
		}
		out := make([]*knowledgev1.Node, 0, len(f.nodes))
		for _, n := range f.nodes {
			if want[n.GetId()] {
				out = append(out, n)
			}
		}
		return &knowledgev1.ExecuteResponse{Nodes: out}, nil
	}
}

func (f *rawGraphFake) Stats(_ context.Context, req *knowledgev1.StatsRequest) (*knowledgev1.StatsResponse, error) {
	f.statsReads++
	f.targets = append(f.targets, req.GetTarget())
	return &knowledgev1.StatsResponse{GraphStats: f.stats}, nil
}

// rawGraphCorpus is the seeded document: a section heading, the paragraph it
// contains, and an unrelated paragraph that must not win.
func rawGraphCorpus() ([]*knowledgev1.Node, []*knowledgev1.Edge) {
	nodes := []*knowledgev1.Node{
		{
			Id: "sec1", Type: "section", SymbolName: "Connection Pooling",
			Source: "web-collect", Metadata: map[string]string{"heading": "Connection Pooling"},
		},
		{
			// The body repeats the query terms verbatim: BM25 does not stem, so
			// a paragraph saying "connections ... reused" would not match
			// "connection pooling" and this fixture would assert nothing.
			Id: "para1", Type: "paragraph", Source: "web-collect",
			Content:  "connection pooling keeps a bounded set of live connections for reuse",
			Metadata: map[string]string{"url": "https://example.com/doc#pooling"},
		},
		{
			Id: "para2", Type: "paragraph", Source: "web-collect",
			Content: "typography and page layout are unrelated to the query term",
		},
	}
	edges := []*knowledgev1.Edge{
		{FromId: "sec1", ToId: "para1", Type: string(kgtypes.EdgeContains)},
	}
	return nodes, edges
}

func webPDFParams(t *testing.T, args map[string]any) kgtools.CallToolParams {
	t.Helper()
	raw, err := json.Marshal(args)
	require.NoError(t, err)
	return kgtools.CallToolParams{Name: "query", Arguments: raw}
}

// rawPDFCorpus is the pdf-shaped document: chunks whose body lives in
// Description and whose locality lives in page metadata.
//
// IT EXISTS BECAUSE A WEB CORPUS CANNOT EXERCISE THE PDF PATH. The ranked-text
// test below runs once per family, and feeding rawGraphCorpus() to both made the
// pdf arm a second run of the web arm: web nodes carry no page_first/page_last,
// so rawGraphPageSpan returned "" and every pdf-only branch in the renderer
// executed in nothing while the subtest passed.
// It returns NODES ONLY, with no edge half. A pdf graph has no section/paragraph
// CONTAINS structure to return: locality comes from the page keys, not from a
// parent heading, so an edge return would be a permanently-nil second value
// pretending the two families have the same shape.
func rawPDFCorpus() []*knowledgev1.Node {
	return []*knowledgev1.Node{
		{
			// Terms repeated verbatim for the same reason the web corpus does it:
			// BM25 does not stem.
			Id: "chunk1", Type: "chunk", Source: "pdf-collect",
			Description: "connection pooling keeps a bounded set of live connections for reuse",
			Metadata:    map[string]string{"page_first": "4", "page_last": "5"},
		},
		{
			Id: "chunk2", Type: "chunk", Source: "pdf-collect",
			Description: "typography and page layout are unrelated to the query term",
			Metadata:    map[string]string{"page_first": "9", "page_last": "9"},
		},
	}
}

// TestRouteWebPDF_RankedTextReturnsRankedResults proves a web/pdf ranked text
// query returns RANKED RESULTS with the arm disclosure, rather than the
// retirement notice it used to return.
//
// Each family brings its OWN corpus and its own locality expectation, because
// the two families locate a hit differently: web through the containing
// section's heading, pdf through the chunk's page span.
//
// WHAT THIS TEST NOW OWNS, AFTER THE SEGMENT CUTOVER: the RENDERING of a hit for
// each family. Which node wins is the segment engine's job and is asserted where
// that engine is driven; here the ranked set is supplied by the fake searcher, so
// the non-matching node is absent because it was never ranked in. The claim kept
// alive here is that a ranked id survives hydrate, heading resolution and render
// with its family's locality intact.
func TestRouteWebPDF_RankedTextReturnsRankedResults(t *testing.T) {
	webNodes, webEdges := rawGraphCorpus()
	pdfNodes := rawPDFCorpus()

	cases := []struct {
		graph        string
		nodes        []*knowledgev1.Node
		edges        []*knowledgev1.Edge
		wantHit      string
		wantAbsent   string
		wantLocality string
	}{
		{
			graph: "web", nodes: webNodes, edges: webEdges,
			wantHit: "para1", wantAbsent: "para2",
			// The heading came from the CONTAINS parent, which is the whole point
			// of the parent lookup — a paragraph node has no name of its own.
			wantLocality: "Connection Pooling",
		},
		{
			graph: "pdf", nodes: pdfNodes, edges: nil,
			wantHit: "chunk1", wantAbsent: "chunk2",
			// The pdf-only branch: rendered only when the node carries the page
			// keys the pdf emitter writes and the web emitter never does.
			wantLocality: "pp. 4-5",
		},
	}

	for _, tc := range cases {
		t.Run(tc.graph, func(t *testing.T) {
			fake := &rawGraphFake{nodes: tc.nodes, edges: tc.edges, graphNames: []string{"doc-slug"}}
			mgr := &fakeSegmentSearcher{hits: []searchengine.Hit{{ID: tc.wantHit, Score: 0.9}}}
			deps := interceptTestDeps{gc: fake, searcher: mgr}

			handled, res := routeWebPDFClient(opCtx(), deps,
				queryArgs{Graph: tc.graph, Name: "doc-slug", Text: "connection pooling"},
				webPDFParams(t, map[string]any{"graph": tc.graph, "name": "doc-slug", "text": "connection pooling"}).Arguments)
			require.True(t, handled, "%s ranked text must be claimed", tc.graph)
			body := textBodyTools(res)

			assert.NotContains(t, body, "retired", "the retirement notice must be gone")
			// No embedder is wired on these deps, so no vector arm ran and the
			// COMPUTED label must say so — the same string the retired hardcoded
			// literal used to print, now derived rather than asserted.
			assert.Contains(t, body, "_search mode: BM25-only_",
				"the arm disclosure must report the arms that actually ran")
			assert.Contains(t, body, tc.wantHit, "the ranked node must be rendered")
			assert.NotContains(t, body, tc.wantAbsent, "an unranked node must not be returned")
			assert.Contains(t, body, tc.wantLocality,
				"the %s family's locality context must be rendered", tc.graph)
			assert.Equal(t, int64(1), mgr.calls.Load(),
				"the ranked read must go through the segment engine")

			// The HYDRATE must target the NAMED graph: a raw graph is keyed by its
			// source slug and there is no default instance. The catalog read runs
			// first and carries no instance, so this reads the ids[] reads alone.
			require.NotEmpty(t, fake.idTargets, "no ids[] hydrate was issued")
			assert.Equal(t, tc.graph, fake.idTargets[0].GetGraph())
			assert.Equal(t, "doc-slug", fake.idTargets[0].GetName())
		})
	}
}

// TestRouteWebPDF_JSONArmCarriesTheSyntheticHeadingKey covers the format:"json"
// arm, which had no assertion at all, and with it the only reason
// nodeWithParentHeading exists.
//
// THE SYNTHETIC KEY IS THE POINT. The text renderer receives the heading as a
// field beside the result, so it needs no metadata key; the JSON arm copies
// Node.Metadata verbatim and would otherwise drop the heading entirely. A hit
// whose heading vanished in JSON while rendering fine in text is exactly the
// asymmetry this asserts against.
func TestRouteWebPDF_JSONArmCarriesTheSyntheticHeadingKey(t *testing.T) {
	nodes, edges := rawGraphCorpus()

	// Rows are looked up BY ID rather than by position. Ranking is asserted by
	// the ranked-text test above; here the section node legitimately outranks the
	// paragraph it contains (its SymbolName is the query), and pinning results[0]
	// would make this test fail on a ranking change it makes no claim about.
	runJSON := func(t *testing.T, text string, fields []string) map[string]map[string]any {
		t.Helper()
		fake := &rawGraphFake{nodes: nodes, edges: edges, graphNames: []string{"doc-slug"}}
		// BOTH PARAGRAPHS ARE RANKED IN AND THE SECTION IS NOT, deliberately: every
		// row these legs look up is a paragraph, and leaving sec1 out of the ranked
		// set means para1's heading can only arrive through the PARENT HYDRATE. A
		// ranked section would have been handed back by the hit hydrate and the
		// heading would resolve either way.
		mgr := &fakeSegmentSearcher{hits: []searchengine.Hit{
			{ID: "para1", Score: 0.9}, {ID: "para2", Score: 0.8},
		}}
		args := map[string]any{"graph": "web", "name": "doc-slug", "text": text, "format": "json"}
		a := queryArgs{Graph: "web", Name: "doc-slug", Text: text, Format: "json"}
		if len(fields) > 0 {
			args["fields"] = fields
			a.Fields = fields
		}
		handled, res := routeWebPDFClient(opCtx(), interceptTestDeps{gc: fake, searcher: mgr}, a,
			webPDFParams(t, args).Arguments)
		require.True(t, handled)

		var decoded map[string]any
		body := textBodyTools(res)
		require.NoError(t, json.Unmarshal([]byte(body), &decoded),
			"the json arm must emit parseable JSON, got: %s", body)
		results, _ := decoded["results"].([]any)
		require.NotEmpty(t, results, "the json arm returned no results")

		byID := make(map[string]map[string]any, len(results))
		for _, r := range results {
			row, ok := r.(map[string]any)
			require.True(t, ok, "a json result row is not an object")
			id, _ := row["id"].(string)
			byID[id] = row
		}
		return byID
	}

	t.Run("heading_appears_under_the_synthetic_key", func(t *testing.T) {
		rows := runJSON(t, "connection pooling", nil)
		row, ok := rows["para1"]
		require.True(t, ok, "the matching paragraph is absent from the json results")

		md, _ := row["metadata"].(map[string]any)
		require.NotNil(t, md, "the json row carries no metadata map")
		assert.Equal(t, "Connection Pooling", md["__parent_heading"],
			"the resolved heading must reach the json arm under the synthetic key")
		// The node's own metadata must survive alongside the synthetic key —
		// nodeWithParentHeading rebuilds the map, so a botched copy would drop it.
		assert.Equal(t, "https://example.com/doc#pooling", md["url"],
			"stamping the synthetic key must not drop the node's own metadata")
	})

	t.Run("the_key_stays_out_of_the_way_when_there_is_no_parent", func(t *testing.T) {
		// KNOWN NEGATIVE for the assertion above: para2 has no CONTAINS parent,
		// so a build that stamped the key unconditionally — or stamped every row
		// with the first heading it resolved — fails here while passing above.
		rows := runJSON(t, "typography layout", nil)
		row, ok := rows["para2"]
		require.True(t, ok, "the parentless paragraph is absent from the json results")

		if md, ok := row["metadata"].(map[string]any); ok {
			_, present := md["__parent_heading"]
			assert.False(t, present,
				"a parentless hit must carry NO synthetic heading key, not an empty one")
		}
	})

	t.Run("the_key_projects_under_fields", func(t *testing.T) {
		rows := runJSON(t, "connection pooling", []string{"id", "metadata.__parent_heading"})
		row, ok := rows["para1"]
		require.True(t, ok, "the matching paragraph is absent from the projected json results")

		assert.Equal(t, "Connection Pooling", row["metadata.__parent_heading"],
			"a per-key projection must reach the synthetic key like any other metadata key")
		// The projection must actually NARROW; a row still carrying the full
		// payload would satisfy the assertion above while proving nothing.
		assert.NotContains(t, row, "content", "the projection did not narrow the row")
		assert.NotContains(t, row, "metadata", "the projection emitted the whole metadata map")
	})
}

// TestRouteWebPDF_IndexFreeOpsFallThrough proves the claimed set did NOT widen:
// by-id, type-browse and the bare call still fall through to engine dispatch.
//
// mode:stats and mode:modules are deliberately absent from this list — both are
// now CLAIMED. The stats test below asserts the first;
// TestWebPDFModules_ReportsPerGraphCollectStamp asserts the second. modules was
// pinned here as index-free until the listing existed, because engine dispatch
// could lower it to GRAPH_NAMES; it is claimed now because that envelope's
// sync_time comes from a different stamper than the collect and so cannot say
// how stale a raw graph is.
func TestRouteWebPDF_IndexFreeOpsFallThrough(t *testing.T) {
	for _, graph := range []string{"web", "pdf"} {
		for _, args := range []queryArgs{
			{Graph: graph, ID: "n1"},        // by-id getNode
			{Graph: graph, Type: "finding"}, // type-browse
			{Graph: graph},                  // bare
		} {
			fake := &rawGraphFake{}
			// Driven through the shared gated wrapper so this test asserts
			// ROUTING rather than restating the accounting gate, which has its
			// own suite.
			handled, _ := gatedRouteWebPDF(opCtx(), interceptTestDeps{gc: fake}, args)
			assert.False(t, handled, "%s index-free op %+v must fall through to engineDispatch", graph, args)
			assert.Zero(t, fake.nodeReads, "a fall-through op must issue no read")
		}
	}
}

// TestInterceptQueryWebPDF_StatsRenderedClientSide proves mode:stats is served
// from one Stats RPC — it reached nothing before, meeting the generic deny — and
// that a nameless call is refused with a message a reader can act on.
func TestInterceptQueryWebPDF_StatsRenderedClientSide(t *testing.T) {
	t.Run("named graph renders from one stats rpc", func(t *testing.T) {
		fake := &rawGraphFake{stats: &knowledgev1.GraphStats{
			NodeCount: 12, EdgeCount: 7,
			NodesByType: map[string]int64{"page": 1, "paragraph": 11},
		}}
		handled, res := InterceptQueryPracticeLinkage(opCtx(), interceptTestDeps{gc: fake},
			webPDFParams(t, map[string]any{"graph": "web", "name": "doc-slug", "mode": "stats"}))
		require.True(t, handled, "web mode:stats must be claimed, not denied")
		body := textBodyTools(res)

		assert.Contains(t, body, "## Web Graph: doc-slug")
		assert.Contains(t, body, "12", "the node count must render")
		assert.Equal(t, 1, fake.statsReads, "exactly one Stats RPC")
		// EXACTLY ONE bounded node read, and it is the collector_schema_version
		// resolution off the graph's root (Limit 1, SkipTotal). This assertion
		// used to require ZERO; the property it was written to protect is
		// "the stats arm must not DRAIN the graph", and a drain pages the whole
		// graph rather than reading one root. One is the ceiling, so a
		// per-node-type loop or a re-introduced drain still fails here.
		assert.Equal(t, 1, fake.nodeReads,
			"the stats arm reads only the graph root for its schema version — never a drain")
	})

	t.Run("pdf uses its own header", func(t *testing.T) {
		fake := &rawGraphFake{stats: &knowledgev1.GraphStats{NodeCount: 3}}
		handled, res := InterceptQueryPracticeLinkage(opCtx(), interceptTestDeps{gc: fake},
			webPDFParams(t, map[string]any{"graph": "pdf", "name": "paper", "mode": "stats"}))
		require.True(t, handled)
		assert.Contains(t, textBodyTools(res), "## PDF Graph: paper")
	})

	// The refusal must do TWO things, and asserting only "it errored" would be
	// satisfied by any error at all.
	t.Run("nameless call is refused with an actionable message", func(t *testing.T) {
		fake := &rawGraphFake{}
		handled, res := InterceptQueryPracticeLinkage(opCtx(), interceptTestDeps{gc: fake},
			webPDFParams(t, map[string]any{"graph": "web", "mode": "stats"}))
		require.True(t, handled, "the nameless shape is still claimed — it is refused, not passed through")
		body := textBodyTools(res)

		assert.Contains(t, body, "name", "the refusal must name the missing param")
		assert.Contains(t, body, `mode:"modules"`, "the refusal must point at the enumeration surface")
		assert.Zero(t, fake.statsReads, "the refusal must precede the Stats RPC, costing no round trip")
	})

	// KNOWN-NEGATIVE for the header assertions above: a graph the arm does not
	// serve must not be claimed here at all, so "web renders a header" is not
	// just "this intercept renders a header for anything".
	t.Run("a foreign graph is not claimed by this arm", func(t *testing.T) {
		fake := &rawGraphFake{stats: &knowledgev1.GraphStats{NodeCount: 1}}
		handled, res := InterceptQueryPracticeLinkage(opCtx(), interceptTestDeps{gc: fake},
			webPDFParams(t, map[string]any{"graph": "code", "name": "repo", "mode": "stats"}))
		if handled {
			assert.NotContains(t, strings.ToLower(textBodyTools(res)), "web graph")
		}
	})
}
