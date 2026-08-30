// SPDX-License-Identifier: Apache-2.0

package web

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
)

// emitFromPage converts a *pageRecord into flat slices of *knowledgev1.Node
// and kgwire.BatchEdge suitable for handing to a batch create. The root
// node is a "page" node; every section/content record becomes a node
// with an EdgeContains from its parent carrying a zero-based `position`
// metadata key. One "raw_html" node per page holds the retained response
// bytes and is contained by the page after its last section. Internal links
// become EdgeReferences edges from the page; external cites become
// EdgeReferences edges with rel="external" metadata.
//
// Node IDs are deterministic: sha256(page_url || kind || path || idx)
// truncated to 16 hex chars. Re-running the emitter on the same
// pageRecord yields identical IDs, which lets the pipeline dedupe and
// lets callers cross-link pages without coordination.
func emitFromPage(p *pageRecord) ([]*knowledgev1.Node, []kgwire.BatchEdge) {
	if p == nil {
		return nil, nil
	}
	e := newEmitter(p)
	e.emitPageNode()
	for i, sec := range p.TopSections {
		e.emitSection(e.pageID, "", sec, i, e.pageURI)
	}
	// Appended LAST, at position len(TopSections), so the retained-HTML node
	// arrives without shifting any section's contains-position.
	e.emitRawHTML(len(p.TopSections))
	e.emitLinks()
	return e.nodes, e.edges
}

// emitter carries shared state across per-record emission: the node/edge
// accumulators, the page URL used to derive stable IDs, and pageURI — the
// address the content was served from after redirects, which every emitted
// node carries under the `uri` metadata key.
type emitter struct {
	page    *pageRecord
	pageID  string
	pageURI string
	nodes   []*knowledgev1.Node
	edges   []kgwire.BatchEdge
}

func newEmitter(p *pageRecord) *emitter {
	return &emitter{
		page:    p,
		pageID:  stableID(p.URL, "page", "", 0),
		pageURI: p.FinalURL,
	}
}

// emitPageNode appends the root page node with page-level metadata.
//
// Description is an UNTRUNCATED flatten of TopSections' paragraph +
// list-item text into a single body string. Without it, recipes translating
// page → pattern (azure-patterns, hohpe-eip, etc.) only see the bare title —
// the resulting practice nodes carry no body content, so BM25 and HNSW
// indexes have nothing to match query tokens against and the patterns
// silently drop out of search.
//
// The flatten's content-type skips (code blocks, tables, images, quotes) are
// PROJECTION DESIGN — a choice about which record kinds belong in a prose
// summary — not a length limit. Nothing here bounds the Description's size.
func (e *emitter) emitPageNode() {
	md := map[string]string{
		"url":          e.page.URL,
		"final_url":    e.page.FinalURL,
		"http_status":  strconv.Itoa(e.page.HTTPStatus),
		"content_hash": e.page.ContentHash,
		"uri":          e.pageURI,
	}
	if e.page.Title != "" {
		md["title"] = e.page.Title
	}
	if e.page.Author != "" {
		md["author"] = e.page.Author
	}
	if e.page.PubDate != "" {
		md["pub_date"] = e.page.PubDate
	}
	if !e.page.FetchedAt.IsZero() {
		md["fetched_at"] = e.page.FetchedAt.UTC().Format("2006-01-02T15:04:05Z07:00")
	}
	applyCommonAttrs(md, e.page.Attrs)
	e.nodes = append(e.nodes, &knowledgev1.Node{
		Id:          e.pageID,
		Type:        "page",
		SymbolName:  e.page.Title,
		Description: flattenPageBody(e.page.TopSections),
		Source:      "web-collect",
		Metadata:    md,
	})
}

// flattenPageBody walks sections in document order and concatenates
// paragraph + list-item text with section headings as separators. Keeps
// headings inline ("## Heading\nbody...") so the cross-encoder reads the
// structure as natural documentation. Code blocks, tables, images, and
// quotes are skipped — they're typically shape rather than the searchable
// text the body summary needs to surface.
func flattenPageBody(sections []*sectionRecord) string {
	if len(sections) == 0 {
		return ""
	}
	var b strings.Builder
	for _, sec := range sections {
		appendSectionBody(&b, sec)
	}
	return strings.TrimSpace(b.String())
}

// appendSectionBody writes one section's heading + content to b, recursing
// into nested sections.
func appendSectionBody(b *strings.Builder, sec *sectionRecord) {
	if sec == nil {
		return
	}
	if sec.Heading != "" {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(strings.Repeat("#", sec.Depth))
		b.WriteByte(' ')
		b.WriteString(sec.Heading)
		b.WriteByte('\n')
	}
	for _, child := range sec.Children {
		switch r := child.(type) {
		case paragraphRecord:
			b.WriteString(r.Text)
			b.WriteByte('\n')
		case listRecord:
			for _, item := range r.Items {
				b.WriteString("- ")
				b.WriteString(item.Text)
				b.WriteByte('\n')
			}
		case nestedSectionRecord:
			appendSectionBody(b, r.Section)
		}
	}
}

// emitSection emits a section node plus every child record in document
// order. path threads through deterministic ID derivation so sibling
// sections at the same position don't collide.
//
// uri is the enclosing scope's address. A section carrying its own heading
// anchor addresses itself from the page base, so its fragment REPLACES any
// inherited one; an anchorless section inherits its parent's uri unchanged.
// Either way the resulting uri is stamped on this section's node and threaded
// down to every child record.
func (e *emitter) emitSection(parentID, path string, sec *sectionRecord, idx int, uri string) {
	if sec == nil {
		return
	}
	myPath := extendPath(path, "section", idx)
	id := stableID(e.page.URL, "section", myPath, idx)
	myURI := uri
	if sec.Anchor != "" {
		myURI = e.pageURI + "#" + sec.Anchor
	}
	md := map[string]string{
		"heading": sec.Heading,
		"depth":   strconv.Itoa(sec.Depth),
		"uri":     myURI,
	}
	if sec.Anchor != "" {
		md["anchor"] = sec.Anchor
	}
	applyCommonAttrs(md, sec.Attrs)
	e.nodes = append(e.nodes, &knowledgev1.Node{
		Id:         id,
		Type:       "section",
		SymbolName: sec.Heading,
		Source:     "web-collect",
		Metadata:   md,
	})
	e.addContains(parentID, id, idx)

	for i, child := range sec.Children {
		e.emitContent(id, myPath, child, i, myURI)
	}
}

// emitContent dispatches a contentRecord to its kind-specific helper,
// forwarding the enclosing section's uri so every emitted node carries it.
func (e *emitter) emitContent(parentID, path string, rec contentRecord, idx int, uri string) {
	switch r := rec.(type) {
	case nestedSectionRecord:
		e.emitSection(parentID, path, r.Section, idx, uri)
	case paragraphRecord:
		e.emitParagraph(parentID, path, r, idx, uri)
	case codeBlockRecord:
		e.emitCodeBlock(parentID, path, r, idx, uri)
	case listRecord:
		e.emitList(parentID, path, r, idx, uri)
	case tableRecord:
		e.emitTable(parentID, path, r, idx, uri)
	case linkRecord:
		e.emitLink(parentID, path, r, idx, uri)
	case imageRecord:
		e.emitImage(parentID, path, r, idx, uri)
	case quoteRecord:
		e.emitQuote(parentID, path, r, idx, uri)
	}
}

// emitLinks adds page-level EdgeReferences for every internal link and
// external cite. External cites carry rel="external" metadata so callers
// can distinguish the two classes on the edge alone.
func (e *emitter) emitLinks() {
	for _, u := range e.page.InternalLinks {
		e.edges = append(e.edges, kgwire.BatchEdge{
			FromIdx:  -1,
			ToIdx:    -1,
			FromID:   e.pageID,
			ToID:     "web:url:" + u,
			Type:     kgtypes.EdgeReferences,
			Method:   "web-collect",
			Evidence: jsonMeta(map[string]string{"rel": "internal", "url": u}),
		})
	}
	for _, c := range e.page.ExternalCites {
		e.edges = append(e.edges, kgwire.BatchEdge{
			FromIdx:  -1,
			ToIdx:    -1,
			FromID:   e.pageID,
			ToID:     "web:url:" + c.URL,
			Type:     kgtypes.EdgeReferences,
			Method:   "web-collect",
			Evidence: jsonMeta(map[string]string{"rel": "external", "url": c.URL}),
		})
	}
}

// addContains appends a parent→child EdgeContains edge with a
// zero-based `position` on Evidence so downstream consumers can
// reconstruct document order.
func (e *emitter) addContains(parentID, childID string, pos int) {
	e.edges = append(e.edges, kgwire.BatchEdge{
		FromIdx:  -1,
		ToIdx:    -1,
		FromID:   parentID,
		ToID:     childID,
		Type:     kgtypes.EdgeContains,
		Method:   "web-collect",
		Evidence: jsonMeta(map[string]string{"position": strconv.Itoa(pos)}),
	})
}

// stableID returns a 16-hex-char deterministic identifier derived from
// the page URL, record kind, structural path, and sibling index.
func stableID(pageURL, kind, path string, idx int) string {
	sum := sha256.Sum256([]byte(pageURL + "|" + kind + "|" + path + "|" + strconv.Itoa(idx)))
	return hex.EncodeToString(sum[:8])
}

// extendPath appends a kind/idx segment to path so nested emission
// yields unique IDs even when two sibling records of different kinds
// share the same index.
func extendPath(path, kind string, idx int) string {
	seg := kind + strconv.Itoa(idx)
	if path == "" {
		return seg
	}
	return path + "/" + seg
}

// jsonMeta marshals a small string map into JSON for edge Evidence
// storage. On marshal error (never in practice for string→string maps)
// returns "".
func jsonMeta(m map[string]string) string {
	if len(m) == 0 {
		return ""
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return ""
	}
	return string(raw)
}
