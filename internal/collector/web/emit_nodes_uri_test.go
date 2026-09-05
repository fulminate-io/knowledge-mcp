// SPDX-License-Identifier: Apache-2.0

package web

import (
	"strings"
	"testing"
	"time"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// This file fences the `uri` metadata key the web collector stamps on every
// node it emits. It lives apart from emit_nodes_test.go so neither file
// approaches the repo's 500-line hard cap.

const (
	uriTestPageURL  = "https://example.com/doc"
	uriTestFinalURL = "https://example.com/final-doc"
)

// uriTestPage builds a pageRecord exercising every emitter arm: an anchored
// top-level section, an anchorless nested section (inheritance), an anchored
// nested section (fragment replacement rather than append), an anchorless
// top-level section, and one record of each content kind.
func uriTestPage() *pageRecord {
	deepAnchored := &sectionRecord{
		Heading: "Gamma",
		Depth:   3,
		Anchor:  "gamma",
		Children: []contentRecord{
			paragraphRecord{Text: "gamma prose"},
		},
	}
	nestedPlain := &sectionRecord{
		Heading: "Beta",
		Depth:   2,
		Children: []contentRecord{
			paragraphRecord{Text: "beta prose"},
			codeBlockRecord{Language: "go", Source: "package beta"},
			listRecord{Kind: "ul", Items: []listItemRecord{
				{Text: "beta item one", Position: 0},
				{Text: "beta item two", Position: 1},
			}},
			tableRecord{Headers: []string{"h"}, Rows: [][]string{{"c"}}},
			imageRecord{URL: "https://cdn.example.com/pic.png", Alt: "pic"},
			quoteRecord{Text: "beta quote"},
			nestedSectionRecord{Section: deepAnchored},
		},
	}
	anchoredTop := &sectionRecord{
		Heading: "Alpha",
		Depth:   1,
		Anchor:  "alpha",
		Children: []contentRecord{
			paragraphRecord{Text: "alpha prose"},
			// The two-anchor hazard's specimen: the TARGET url carries its
			// own fragment, which must never become this node's uri.
			linkRecord{
				URL:    "https://other.example.org/elsewhere#target-fragment",
				Text:   "see elsewhere",
				Rel:    "external",
				Anchor: "target-fragment",
			},
			nestedSectionRecord{Section: nestedPlain},
		},
	}
	plainTop := &sectionRecord{
		Heading: "Delta",
		Depth:   1,
		Children: []contentRecord{
			paragraphRecord{Text: "delta prose"},
		},
	}
	return &pageRecord{
		URL:         uriTestPageURL,
		FinalURL:    uriTestFinalURL,
		Title:       "URI fixture",
		ContentHash: "deadbeef",
		HTTPStatus:  200,
		TopSections: []*sectionRecord{anchoredTop, plainTop},
	}
}

// uriOfNode returns the uri metadata of the first node matching nodeType and
// the given identifying text, which is matched against Content and
// SymbolName. It fails the test when no such node exists.
func uriOfNode(t *testing.T, nodes []*knowledgev1.Node, nodeType, ident string) string {
	t.Helper()
	for _, n := range nodes {
		if n.Type != nodeType {
			continue
		}
		if n.Content != ident && n.SymbolName != ident {
			continue
		}
		return n.Metadata["uri"]
	}
	t.Fatalf("no %s node identified by %q among %d nodes", nodeType, ident, len(nodes))
	return ""
}

func TestEmitFromPage_URIStampedOnEveryNode(t *testing.T) {
	nodes, _ := mustEmitFromPage(t, uriTestPage(), time.Time{})

	// Known-positive on the node set itself: a vacuous slice would make the
	// "every node carries a uri" loop pass without measuring anything.
	if len(nodes) < 12 {
		t.Fatalf("fixture emitted only %d nodes; the census below would be near-vacuous", len(nodes))
	}

	// 1. EVERY emitted node carries a non-empty uri prefixed by the page's
	//    final URL. Reported per-node so a single missed emitter is named.
	missing := 0
	for _, n := range nodes {
		got := n.Metadata["uri"]
		if got == "" {
			t.Errorf("node id=%s type=%s carries no uri (metadata=%v)", n.Id, n.Type, n.Metadata)
			missing++
			continue
		}
		if got != uriTestFinalURL && !strings.HasPrefix(got, uriTestFinalURL+"#") {
			t.Errorf("node id=%s type=%s uri=%q is not addressed from the page base %q",
				n.Id, n.Type, got, uriTestFinalURL)
		}
	}
	if missing > 0 {
		t.Fatalf("%d of %d emitted nodes carry no uri", missing, len(nodes))
	}

	// 2. The page node's uri is the final URL, bare.
	if got := uriOfNode(t, nodes, "page", "URI fixture"); got != uriTestFinalURL {
		t.Errorf("page uri = %q, want %q", got, uriTestFinalURL)
	}

	// 3. An anchored section addresses itself from the page base.
	alphaURI := uriOfNode(t, nodes, "section", "Alpha")
	wantAlpha := uriTestFinalURL + "#alpha"
	if alphaURI != wantAlpha {
		t.Fatalf("anchored section uri = %q, want %q", alphaURI, wantAlpha)
	}

	// 4. A child of that section INHERITS the fragmented uri.
	if got := uriOfNode(t, nodes, "paragraph", "alpha prose"); got != wantAlpha {
		t.Errorf("paragraph in anchored section: uri = %q, want inherited %q", got, wantAlpha)
	}

	// 5. A nested ANCHORLESS section inherits its parent's uri, and so do its
	//    children — every content kind, so no emitter arm is unmeasured.
	if got := uriOfNode(t, nodes, "section", "Beta"); got != wantAlpha {
		t.Errorf("anchorless nested section: uri = %q, want inherited %q", got, wantAlpha)
	}
	for _, tc := range []struct{ nodeType, ident string }{
		{"paragraph", "beta prose"},
		{"code_block", "package beta"},
		{"list_item", "beta item one"},
		{"list_item", "beta item two"},
		{"blockquote", "beta quote"},
	} {
		if got := uriOfNode(t, nodes, tc.nodeType, tc.ident); got != wantAlpha {
			t.Errorf("%s %q: uri = %q, want inherited %q", tc.nodeType, tc.ident, got, wantAlpha)
		}
	}
	// list, table and image carry no identifying text, so locate them by type
	// and assert the inherited uri on each.
	for _, nodeType := range []string{"list", "table", "image"} {
		found := false
		for _, n := range nodes {
			if n.Type != nodeType {
				continue
			}
			found = true
			if n.Metadata["uri"] != wantAlpha {
				t.Errorf("%s node: uri = %q, want inherited %q", nodeType, n.Metadata["uri"], wantAlpha)
			}
		}
		if !found {
			t.Errorf("fixture emitted no %s node, so its uri stamp is unmeasured", nodeType)
		}
	}

	// 6. An anchored NESTED section REPLACES the inherited fragment rather
	//    than appending to it — a fragment is an address from the page base.
	gammaURI := uriOfNode(t, nodes, "section", "Gamma")
	wantGamma := uriTestFinalURL + "#gamma"
	if gammaURI != wantGamma {
		t.Errorf("anchored nested section: uri = %q, want %q (fragment replaced, not appended)",
			gammaURI, wantGamma)
	}
	if got := uriOfNode(t, nodes, "paragraph", "gamma prose"); got != wantGamma {
		t.Errorf("paragraph under anchored nested section: uri = %q, want %q", got, wantGamma)
	}

	// 7. An anchorless top-level section inherits the bare page uri.
	if got := uriOfNode(t, nodes, "section", "Delta"); got != uriTestFinalURL {
		t.Errorf("anchorless top-level section: uri = %q, want %q", got, uriTestFinalURL)
	}

	// 8. THE TWO-ANCHOR HAZARD. A link node's uri is its own position in THIS
	//    document — the enclosing section's uri — never its target's address.
	//    md["url"] and md["anchor"] keep describing the target.
	var link *knowledgev1.Node
	for _, n := range nodes {
		if n.Type == "link" {
			link = n
			break
		}
	}
	if link == nil {
		t.Fatal("fixture emitted no link node; the two-anchor assertion below is vacuous")
	}
	if link.Metadata["uri"] != wantAlpha {
		t.Errorf("link uri = %q, want the enclosing section's %q", link.Metadata["uri"], wantAlpha)
	}
	if link.Metadata["uri"] == link.Metadata["url"] {
		t.Errorf("link uri %q equals its target url — the target was mistaken for the node's address",
			link.Metadata["uri"])
	}
	if got := link.Metadata["url"]; got != "https://other.example.org/elsewhere#target-fragment" {
		t.Errorf("link url = %q, want the target url unchanged", got)
	}
	if got := link.Metadata["anchor"]; got != "target-fragment" {
		t.Errorf("link anchor = %q, want the target fragment unchanged", got)
	}
}
