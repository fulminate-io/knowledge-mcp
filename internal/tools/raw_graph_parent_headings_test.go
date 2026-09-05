// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"sync/atomic"
	"testing"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// rawGraphParentHeadings takes an engine.ExecuteFn directly, so this test drives
// it with a stub closure rather than standing up a connect server. That is
// deliberate: the shared canned harness returns ONE fixed ExecuteResponse for
// every call, which cannot serve a node read and an edge read distinctly, and
// this test needs exactly that distinction.

// rawTestGraphName is the graph name the stub below is called with, kept as a
// constant so the selector is built in one place rather than spelled per call.
const rawTestGraphName = "doc"

// rawTestTarget is the selector every stub below is called with.
func rawTestTarget() *knowledgev1.GraphSelector {
	return &knowledgev1.GraphSelector{Graph: "web", Name: rawTestGraphName}
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

// TestRawGraphDiscovery_ParentHeadingFromContainsEdge proves a paragraph hit
// picks up its section heading from ONE pivot edge read, and that a parentless
// hit gets an absent heading rather than an invented one.
//
// IT PINS THE CALLER-ALREADY-HOLDS-THE-PARENTS CASE. rawGraphParentHeadings now
// hydrates the parents the CONTAINS edges name and byID does not already hold,
// because a segment-backed caller hydrates only its hits. This fixture supplies
// every parent up front, so the hydrate has nothing to fetch and must be SKIPPED
// — which is the property that keeps a caller holding a fuller map at its
// original round-trip count. The complementary case, a parent that is NOT
// resident and must be fetched, is gated by the web_heading leg of
// TestRawGraphHybridArm_ServesHybridThroughSegments.
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
	// Every parent was already in byID, so the hydrate had nothing to fetch and
	// must not have run. A hydrate issued anyway would be a wasted round trip on
	// every caller that already holds its parents.
	if n := nodeReads.Load(); n != 0 {
		t.Errorf("the parent lookup issued %d node read(s) with every parent already resident; "+
			"the hydrate must be skipped when nothing is missing", n)
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
