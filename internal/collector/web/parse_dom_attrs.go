// SPDX-License-Identifier: Apache-2.0

package web

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/net/html"
)

// commonAttrs captures the raw signals the scraper preserves verbatim on
// every emitted structural node. Scraper makes no judgment about what these
// mean — transformers interpret them per their target schema.
//
// TAG AND DOMDEPTH COME FROM THE ELEMENT ITSELF, NOT FROM ITS ATTRIBUTES,
// which is why an attribute-free element no longer yields the zero value:
// Class, ID, Role and Data are read off n.Attr and stay empty when there are
// no attributes to read, while Tag is the element's own name and DomDepth is
// its position in the tree. A record built from a real element therefore
// always carries those two, and a record with no source element carries
// neither — which is what makes an absent tag a readable statement that the
// node is synthetic rather than an omission.
type commonAttrs struct {
	Class string            // raw class attribute value (empty-omit)
	ID    string            // raw id attribute value (empty-omit)
	Role  string            // ARIA role (empty-omit)
	Data  map[string]string // every data-* attribute, raw values (nil-omit)
	// Tag is the source element's name, e.g. "p", "h2", "div" (empty-omit).
	Tag string
	// DomDepth is the element's ancestor count from the document root.
	DomDepth int

	// AttrSourceTag and AttrSourceDepth name the ANCESTOR the class/id/role/
	// data above were climbed to, and are set ONLY when that ancestor is not
	// the element Tag and DomDepth describe.
	//
	// They exist because an anonymous inline run has no element of its own:
	// its attributes are taken from the nearest classed ancestor, arbitrarily
	// far up, and nothing previously marked that the climb happened. A
	// consumer reading class="outerbox" on a record could not tell whether the
	// author put that class on the record's own element or eleven levels above
	// it. An empty AttrSourceTag is the statement that no climb took place.
	AttrSourceTag   string
	AttrSourceDepth int
}

// extractCommonAttrs reads class/id/role + every data-* attribute from n,
// plus n's own tag name and DOM depth. Returns zero-valued commonAttrs when
// n is nil; an attribute-free element still yields Tag and DomDepth. Does
// NOT filter, lowercase, or interpret — verbatim preservation only.
func extractCommonAttrs(n *html.Node) commonAttrs {
	if n == nil {
		return commonAttrs{}
	}
	out := commonAttrs{Tag: n.Data, DomDepth: nodeDepth(n)}
	for _, a := range n.Attr {
		switch a.Key {
		case "class":
			out.Class = a.Val
		case "id":
			out.ID = a.Val
		case "role":
			out.Role = a.Val
		default:
			if strings.HasPrefix(a.Key, "data-") {
				if out.Data == nil {
					out.Data = make(map[string]string)
				}
				out.Data[strings.TrimPrefix(a.Key, "data-")] = a.Val
			}
		}
	}
	return out
}

// applyCommonAttrs writes a commonAttrs into a metadata map with the keys
// "class" / "id" / "role" / "data" / "tag" / "dom_depth" / "attr_source", plus
// "attr_source_tag" and "attr_source_depth" on a climbed record. Empty strings
// are omitted.
//
// tag and dom_depth are written as a PAIR, guarded on a non-empty Tag: they
// are two readings of one source element, so a record with no element (the
// synthetic page-level raw_html node, the depth-0 root section that sinks
// pre-heading prose) carries neither rather than carrying a depth of zero
// that would be indistinguishable from a genuine document-root element. Data is
// JSON-encoded via encoding/json.Marshal; on the theoretical marshal
// failure the data key is silently skipped. json.Marshal of a
// map[string]string cannot fail in practice — every supported value type
// (string keys, string values) round-trips cleanly — so the silent skip
// is safe: the only way here is a compiler/runtime bug, at which point a
// missing metadata key is the least of our problems.
func applyCommonAttrs(md map[string]string, attrs commonAttrs) error {
	if md == nil {
		return nil
	}
	if attrs.Class != "" {
		md["class"] = attrs.Class
	}
	if attrs.ID != "" {
		md["id"] = attrs.ID
	}
	if attrs.Role != "" {
		md["role"] = attrs.Role
	}
	if len(attrs.Data) > 0 {
		b, err := json.Marshal(attrs.Data)
		if err != nil {
			return fmt.Errorf("marshal data-* attributes: %w", err)
		}
		md["data"] = string(b)
	}
	if attrs.Tag != "" {
		md["tag"] = attrs.Tag
		md["dom_depth"] = strconv.Itoa(attrs.DomDepth)
		if attrs.AttrSourceTag != "" {
			md["attr_source"] = attrSourceAncestor
			md["attr_source_tag"] = attrs.AttrSourceTag
			md["attr_source_depth"] = strconv.Itoa(attrs.AttrSourceDepth)
		} else {
			md["attr_source"] = attrSourceOwn
		}
	}
	return nil
}

// The two values attr_source can take. An element-derived record reports
// "own"; a record whose attributes were climbed to from an ancestor reports
// "ancestor" and names it.
const (
	attrSourceOwn      = "own"
	attrSourceAncestor = "ancestor"
)

// runAttrs builds the commonAttrs for an ANONYMOUS INLINE RUN — a stretch of
// text and inline elements sitting directly in a block box with no wrapper
// element of its own.
//
// THE TWO HALVES COME FROM DIFFERENT ELEMENTS, and that is the whole reason
// this function exists rather than a bare extractCommonAttrs call:
//
//   - tag and dom_depth describe the run's IMMEDIATE containing element, the
//     block box it actually sits in. That is the truthful answer to "where is
//     this text in the document".
//   - class, id, role and data come from nearestAttrSource, the closest
//     ancestor that carries any of them, because the immediate container is
//     frequently unclassed and taking attributes from it yields empty
//     class and id on exactly the records a recipe needs to classify.
//
// When those two elements are not the same one, the climb is RECORDED rather
// than left implicit: AttrSourceTag and AttrSourceDepth name the element the
// attributes came from, so a consumer can see the distance between the text
// and the classification it inherited.
//
// The two node pointers are compared directly rather than the tree being
// walked a second time for the comparison.
func runAttrs(parent *html.Node) commonAttrs {
	container := nearestElement(parent)
	source := nearestAttrSource(parent)

	out := extractCommonAttrs(source)
	out.Tag, out.DomDepth = "", 0
	if container != nil {
		out.Tag, out.DomDepth = container.Data, nodeDepth(container)
	}
	if source != nil && source != container {
		out.AttrSourceTag, out.AttrSourceDepth = source.Data, nodeDepth(source)
	}
	return out
}

// nearestElement returns n when n is an element, else its closest ancestor
// element, else nil. A run sitting directly under the document node has no
// element parent at that level, so the climb is what gives it a tag at all.
func nearestElement(n *html.Node) *html.Node {
	for cur := n; cur != nil; cur = cur.Parent {
		if cur.Type == html.ElementNode {
			return cur
		}
	}
	return nil
}

// nodeDepth returns n's ancestor count from the document root: the number of
// parent hops taken to reach a node with no parent, counting the document
// node itself as one hop.
//
// COUNTING THE DOCUMENT NODE IS WHAT MAKES THIS AGREE WITH THE HEURISTIC's
// OWN NUMBERS. scanForMarkers walks with visit(doc, 0) and increments per
// level (parse_dom_headings.go), so under that counter html is 1, body is 2
// and an <h1> inside <article> is 4. Walking n.Parent until nil reproduces
// exactly that: the h1 hops through article, body, html and doc. A loop that
// stopped at the <html> element would return 3 for the same node and disagree
// with the pre-pass silently.
func nodeDepth(n *html.Node) int {
	depth := 0
	for p := n.Parent; p != nil; p = p.Parent {
		depth++
	}
	return depth
}
