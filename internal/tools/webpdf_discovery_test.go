// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// The discovery units each take an engine.ExecuteFn directly, so these tests
// drive them with stub closures rather than standing up a connect server. That
// is deliberate: the shared canned harness returns ONE fixed ExecuteResponse for
// every call, which cannot serve a node read and an edge read distinctly, and
// the parent-heading test needs exactly that distinction.

// rawTestGraphName is the graph name every stub below is called with. The
// scan-cap test asserts the ceiling error names it, so it is a constant rather
// than a per-test literal.
const rawTestGraphName = "doc"

// rawTestTarget is the selector every stub below is called with.
func rawTestTarget() *knowledgev1.GraphSelector {
	return &knowledgev1.GraphSelector{Graph: "web", Name: rawTestGraphName}
}

// nodePagesExec returns an ExecuteFn serving the given pages of nodes in order,
// one page per call, and counts the calls it received.
func nodePagesExec(pages [][]*knowledgev1.Node, calls *atomic.Int64) engine.ExecuteFn {
	var idx int
	return func(_ context.Context, _ *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
		if calls != nil {
			calls.Add(1)
		}
		if idx >= len(pages) {
			return &knowledgev1.ExecuteResponse{}, nil
		}
		page := pages[idx]
		idx++
		return &knowledgev1.ExecuteResponse{Nodes: page}, nil
	}
}

func webParagraph(id, content string) *knowledgev1.Node {
	// Mirrors the web emitter: a paragraph carries a body in Content and has
	// neither SymbolName nor Description.
	return &knowledgev1.Node{Id: id, Type: "paragraph", Content: content, Source: "web-collect"}
}

func webSection(id, heading string) *knowledgev1.Node {
	// Mirrors the web emitter: a section carries its heading in SymbolName and
	// has NO Content and NO Description at all.
	return &knowledgev1.Node{
		Id: id, Type: "section", SymbolName: heading, Source: "web-collect",
		Metadata: map[string]string{"heading": heading},
	}
}

func pdfChunk(id, description string) *knowledgev1.Node {
	// Mirrors the pdf emitter: a chunk body lands in Description.
	return &knowledgev1.Node{
		Id: id, Type: "chunk", Description: description, Source: "pdf-collect",
		Metadata: map[string]string{"page_first": "4", "page_last": "5"},
	}
}

// TestRawGraphDiscovery_RanksDrainedNodesByBM25 proves the read is RANKED, not
// merely drained: the term-bearing node wins even though it is LAST in drain
// order, and the returned scores descend.
func TestRawGraphDiscovery_RanksDrainedNodesByBM25(t *testing.T) {
	nodes := []*knowledgev1.Node{
		webParagraph("p1", "an unrelated paragraph about gardening and weather"),
		webParagraph("p2", "another unrelated paragraph about cooking and recipes"),
		// The only node mentioning the query term, deliberately placed LAST so
		// a function returning drain order rather than rank order fails here.
		webParagraph("p3", "connection pooling keeps a bounded set of live connections"),
	}
	var calls atomic.Int64
	drained, err := drainRawGraphNodes(context.Background(),
		nodePagesExec([][]*knowledgev1.Node{nodes}, &calls), rawTestTarget(), rawDiscoveryNodeScanCap)
	if err != nil {
		t.Fatalf("drainRawGraphNodes: %v", err)
	}
	if len(drained) != 3 {
		t.Fatalf("drained %d nodes, want 3", len(drained))
	}

	hits, err := rankRawGraphNodes(drained, "connection pooling", 10)
	if err != nil {
		t.Fatalf("rankRawGraphNodes: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("no hits for a term present in the corpus")
	}
	if hits[0].ID != "p3" {
		t.Errorf("top hit = %q, want p3 — results are in drain order, not BM25 rank order", hits[0].ID)
	}
	for i := 1; i < len(hits); i++ {
		if hits[i].Score > hits[i-1].Score {
			t.Errorf("scores ascend at %d: %v then %v", i, hits[i-1].Score, hits[i].Score)
		}
	}
	// A known-positive control for the zero case: a term in no document must
	// produce no hits, so "some hits" above is not just "everything always".
	miss, err := rankRawGraphNodes(drained, "zygomorphic", 10)
	if err != nil {
		t.Fatalf("rankRawGraphNodes(miss): %v", err)
	}
	if len(miss) != 0 {
		t.Errorf("a term in no document returned %d hits, want 0", len(miss))
	}
}

// TestRawGraphDiscovery_ScanCapErrorsRatherThanRankingAPrefix proves the ceiling
// is an ERROR, not a truncation. The control arm at a cap the corpus fits under
// is what makes the error arm readable: without it, a drain that always failed
// would look identical.
func TestRawGraphDiscovery_ScanCapErrorsRatherThanRankingAPrefix(t *testing.T) {
	nodes := []*knowledgev1.Node{
		webParagraph("p1", "one"),
		webParagraph("p2", "two"),
		webParagraph("p3", "three"),
	}

	// CONTROL ARM: a cap the corpus fits under drains cleanly.
	drained, err := drainRawGraphNodes(context.Background(),
		nodePagesExec([][]*knowledgev1.Node{nodes}, nil), rawTestTarget(), 10)
	if err != nil {
		t.Fatalf("control arm at cap=10 over 3 nodes must succeed, got: %v", err)
	}
	if len(drained) != 3 {
		t.Fatalf("control arm drained %d nodes, want 3", len(drained))
	}

	// CEILING ARM: the same corpus under a cap it exceeds.
	got, err := drainRawGraphNodes(context.Background(),
		nodePagesExec([][]*knowledgev1.Node{nodes}, nil), rawTestTarget(), 2)
	if err == nil {
		t.Fatal("cap=2 over 3 nodes returned no error; the ceiling must refuse rather than rank a prefix")
	}
	if got != nil {
		t.Errorf("the ceiling returned %d nodes alongside its error; it must return nil, never a partial slice", len(got))
	}
	if !strings.Contains(err.Error(), "2") {
		t.Errorf("error %q does not name the ceiling that engaged", err)
	}
	if !strings.Contains(err.Error(), rawTestGraphName) {
		t.Errorf("error %q does not name the graph", err)
	}

	// A non-positive cap is a programming error, not "unbounded".
	if _, err := drainRawGraphNodes(context.Background(),
		nodePagesExec([][]*knowledgev1.Node{nodes}, nil), rawTestTarget(), 0); err == nil {
		t.Error("scanCap=0 returned no error; it must never be read as unbounded")
	}
}

// TestRawGraphDiscovery_WebAndPDFBodiesBothScore carries one subtest per FIELD
// the projection must map, because the two emitters put bodies in three
// different places. Each subtest also asserts a non-matching node is absent, so
// a scorer that returned everything could not pass.
func TestRawGraphDiscovery_WebAndPDFBodiesBothScore(t *testing.T) {
	cases := []struct {
		name    string
		nodes   []*knowledgev1.Node
		query   string
		wantTop string
		absent  string
	}{
		{
			name: "web_paragraph_content",
			nodes: []*knowledgev1.Node{
				webParagraph("hit", "the connection pooling section explains bounded reuse"),
				webParagraph("other", "a paragraph about entirely unrelated subject matter"),
			},
			query: "pooling", wantTop: "hit", absent: "other",
		},
		{
			name: "pdf_chunk_description",
			nodes: []*knowledgev1.Node{
				pdfChunk("hit", "this chunk discusses connection pooling at length"),
				pdfChunk("other", "this chunk discusses page layout and typography"),
			},
			query: "pooling", wantTop: "hit", absent: "other",
		},
		{
			// The catcher for dropping FieldSymbolName: this node has NO
			// Content and NO Description, exactly as the emitter builds it, so
			// its heading is the only text it has.
			name: "web_section_symbol_name",
			nodes: []*knowledgev1.Node{
				webSection("hit", "Connection Pooling"),
				webSection("other", "Typography And Layout"),
			},
			query: "pooling", wantTop: "hit", absent: "other",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hits, err := rankRawGraphNodes(tc.nodes, tc.query, 10)
			if err != nil {
				t.Fatalf("rankRawGraphNodes: %v", err)
			}
			if len(hits) == 0 {
				t.Fatalf("no hits — the %s field is not reaching the BM25 field map", tc.name)
			}
			if hits[0].ID != tc.wantTop {
				t.Errorf("top hit = %q, want %q", hits[0].ID, tc.wantTop)
			}
			for _, h := range hits {
				if h.ID == tc.absent {
					t.Errorf("non-matching node %q was returned; the scorer is not discriminating", tc.absent)
				}
			}
		})
	}
}

// TestRawGraphDiscovery_ParentHeadingFromContainsEdge proves a paragraph hit
// picks up its section heading from ONE pivot edge read, resolved against the
// already-drained node set with no hydrate, and that a parentless hit gets an
// absent heading rather than an invented one.
//
// The stub switches on the plan's ReturnMode so the node read and the edge read
// get distinct payloads — the shared canned harness returns one fixed response
// for both and could not serve this.
func TestRawGraphDiscovery_ParentHeadingFromContainsEdge(t *testing.T) {
	section := webSection("s1", "Connection Pooling")
	child := webParagraph("p1", "a bounded set of live connections is reused")
	orphan := webParagraph("p2", "a paragraph nothing contains")
	byID := map[string]*knowledgev1.Node{"s1": section, "p1": child, "p2": orphan}

	var edgeReads, nodeReads atomic.Int64
	exec := func(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
		plan := req.GetQuery()
		if plan.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_EDGES {
			edgeReads.Add(1)
			return &knowledgev1.ExecuteResponse{Edges: []*knowledgev1.Edge{
				{FromId: "s1", ToId: "p1", Type: string(kgtypes.EdgeContains)},
			}}, nil
		}
		nodeReads.Add(1)
		return &knowledgev1.ExecuteResponse{}, nil
	}

	headings, err := rawGraphParentHeadings(context.Background(), exec, rawTestTarget(),
		[]string{"p1", "p2"}, byID)
	if err != nil {
		t.Fatalf("rawGraphParentHeadings: %v", err)
	}
	if got := headings["p1"]; got != "Connection Pooling" {
		t.Errorf("heading for p1 = %q, want %q", got, "Connection Pooling")
	}
	if got, present := headings["p2"]; present && got != "" {
		t.Errorf("parentless hit p2 got heading %q; an absent heading must render as absent, never invented", got)
	}
	// The parents came out of byID, so no node hydrate was issued.
	if n := nodeReads.Load(); n != 0 {
		t.Errorf("the parent lookup issued %d node read(s); parents must come from the drained map", n)
	}
	// One bounded edge read over the hit ids, not one per hit.
	if n := edgeReads.Load(); n != 1 {
		t.Errorf("the parent lookup issued %d edge read(s), want exactly 1", n)
	}

	// KNOWN-NEGATIVE: with no CONTAINS edge in the fixture, the same call must
	// produce no headings at all — so the positive result above cannot be a
	// map that is always populated.
	empty := func(_ context.Context, _ *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
		return &knowledgev1.ExecuteResponse{}, nil
	}
	none, err := rawGraphParentHeadings(context.Background(), empty, rawTestTarget(),
		[]string{"p1", "p2"}, byID)
	if err != nil {
		t.Fatalf("rawGraphParentHeadings(no edges): %v", err)
	}
	if len(none) != 0 {
		t.Errorf("a fixture with no CONTAINS edge produced %d heading(s), want 0", len(none))
	}
}

// TestRawGraphDiscovery_DrainRefusesAnUnstampedOperation moves one requirement
// OUT of the env-gated test below, where it was the only thing asserting it.
//
// That test's own comment says the graph client refuses a covered RPC issued
// with no operation in context, and that "the package fakes never enforced this,
// so only a live daemon shows it". The second half is not true, and the
// distinction matters: the refusal is raised by the client-side interceptor
// BEFORE it calls through to the transport, so any graphclient — including one
// pointed at an address nothing listens on — surfaces it with no daemon at all.
// A daemon is needed to prove the drain RANKS a real corpus; it is not needed to
// prove the drain must be stamped.
//
// THE STAMPED ARM IS WHAT MAKES THE UNSTAMPED ARM READABLE. Both calls fail —
// one cannot dial — so asserting only "it errored" would be satisfied by the
// dial failure alone. The arms are distinguished by WHICH error comes back.
func TestRawGraphDiscovery_DrainRefusesAnUnstampedOperation(t *testing.T) {
	// Port 1 is a reserved port nothing listens on, so a call that gets past the
	// operation guard fails at the transport rather than reaching a real daemon.
	gc := graphclient.NewGraphClientForURL("http://127.0.0.1:1")
	target := rawTestTarget()

	unstamped, err := drainRawGraphNodes(context.Background(), gc.Execute, target, rawDiscoveryNodeScanCap)
	if err == nil {
		t.Fatal("an unstamped covered RPC was accepted; the operation guard did not fire")
	}
	if unstamped != nil {
		t.Errorf("the refusal returned %d nodes alongside its error; it must return nil", len(unstamped))
	}
	if !strings.Contains(err.Error(), "no operation in context") {
		t.Errorf("error %q does not name the missing operation", err)
	}
	if !strings.Contains(err.Error(), "WithOperation") {
		t.Errorf("error %q does not name the remedy, so a reader cannot act on it", err)
	}

	// CONTROL: the same call with the stamp cannot fail the same way. It still
	// fails — nothing is listening — but on the transport, which is what proves
	// the arm above failed on the guard rather than on the address.
	//
	// The deadline bounds the transport's own retry/backoff, which is the only
	// slow thing here; the guard arm above needs no deadline because it never
	// reaches the transport at all. Either failure mode satisfies the assertion,
	// since neither names the operation remedy.
	ctx, cancel := context.WithTimeout(
		graphclient.WithOperation(context.Background(), graphclient.OpQuery),
		500*time.Millisecond)
	defer cancel()
	stamped, err := drainRawGraphNodes(ctx, gc.Execute, target, rawDiscoveryNodeScanCap)
	if err == nil {
		t.Fatalf("the control arm reached something at port 1 and drained %d nodes; "+
			"this test assumes nothing listens there", len(stamped))
	}
	if strings.Contains(err.Error(), "WithOperation") {
		t.Errorf("a STAMPED call still reported the operation guard: %v", err)
	}
}

// TestRawGraphDiscovery_RanksARealLocalGraph is the ONLY check in this feature
// that exercises the ranked read against a real graph rather than a package
// fake. Every other test here drives the units with stub closures, which proves
// CONSTRUCTION and says nothing about operation.
//
// WHAT REMAINS UNCOVERED WHEN THIS SKIPS, stated so the skip is not read as
// nothing being lost. The operation-stamp requirement moved to the test above
// and now runs unconditionally. What still needs a daemon is everything that
// depends on a REAL corpus: that a collected graph drains a non-zero node set
// through the real wire codec, that ranking over real prose puts a term-bearing
// node on top, and that the drain's scan cap is adequate for a real document's
// size. No fake can supply those, because each is a claim about data this
// process did not author. The event that would cover them is a raw graph
// collected on the daemon this test is pointed at.
//
// It is env-gated because raw web and pdf graphs are sync-ineligible by design —
// they are stage-1 intermediates, never summarized, never embedded, never
// synced — so they exist only on the daemon that ingested them. Point
// KNOWLEDGE_RAW_GRAPH_DAEMON_URL at that daemon and name the graph and query to
// run it.
//
// THE GRAPH MUST BE FRESHLY COLLECTED, not merely present. Raw graph files left
// from an older collect are refused by the current loader with a format-version
// error that names its own remedy, so a directory full of old .bin files is not
// a runnable fixture.
//
// The gate is deliberately asymmetric: an unset URL SKIPS, but a URL set without
// the other two variables FAILS. Opting in halfway would otherwise skip silently
// and read as a pass.
func TestRawGraphDiscovery_RanksARealLocalGraph(t *testing.T) {
	daemonURL := os.Getenv("KNOWLEDGE_RAW_GRAPH_DAEMON_URL")
	if daemonURL == "" {
		t.Skip("KNOWLEDGE_RAW_GRAPH_DAEMON_URL unset — see this test's doc comment for the prerequisites")
	}
	graph := os.Getenv("KNOWLEDGE_RAW_GRAPH_TYPE")
	if graph == "" {
		graph = "web"
	}
	name := os.Getenv("KNOWLEDGE_RAW_GRAPH_NAME")
	query := os.Getenv("KNOWLEDGE_RAW_GRAPH_QUERY")
	if name == "" || query == "" {
		t.Fatal("KNOWLEDGE_RAW_GRAPH_DAEMON_URL is set, so KNOWLEDGE_RAW_GRAPH_NAME and KNOWLEDGE_RAW_GRAPH_QUERY must be set too")
	}

	gc := graphclient.NewGraphClientForURL(daemonURL)
	target := &knowledgev1.GraphSelector{Graph: graph, Name: name}
	// The graph client REFUSES a covered RPC whose context carries no
	// operation. In production the tool dispatch stamps it before the composer
	// is reached; a test calling the units directly has to stamp it itself.
	// The package fakes never enforced this, so only a live daemon shows it.
	ctx := graphclient.WithOperation(context.Background(), graphclient.OpQuery)

	nodes, err := drainRawGraphNodes(ctx, gc.Execute, target, rawDiscoveryNodeScanCap)
	if err != nil {
		t.Fatalf("drain %s/%s from %s: %v", graph, name, daemonURL, err)
	}
	if len(nodes) == 0 {
		t.Fatalf("graph %s/%s drained zero nodes — it is empty or was not collected", graph, name)
	}

	hits, err := rankRawGraphNodes(nodes, query, 10)
	if err != nil {
		t.Fatalf("rank: %v", err)
	}
	if len(hits) == 0 {
		t.Fatalf("query %q returned no hits over %d drained nodes", query, len(nodes))
	}

	// The top hit's own text must actually carry the query term. A ranked list
	// alone would not distinguish a working scorer from one returning arbitrary
	// rows in score order.
	byID := make(map[string]*knowledgev1.Node, len(nodes))
	for _, n := range nodes {
		byID[n.GetId()] = n
	}
	top := byID[hits[0].ID]
	if top == nil {
		t.Fatalf("top hit %q is not in the drained set", hits[0].ID)
	}
	haystack := strings.ToLower(top.GetSymbolName() + " " + top.GetSummary() + " " +
		top.GetKeywords() + " " + top.GetDescription() + " " + top.GetContent())
	for term := range strings.FieldsSeq(strings.ToLower(query)) {
		if strings.Contains(haystack, term) {
			return
		}
	}
	t.Errorf("top hit %q contains no term from query %q; the ranking is not driven by the text", hits[0].ID, query)
}
