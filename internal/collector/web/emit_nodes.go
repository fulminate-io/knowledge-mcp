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
// metadata key. Internal links become EdgeReferences edges from the
// page; external cites become EdgeReferences edges with rel="external"
// metadata.
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
		e.emitSection(e.pageID, "", sec, i)
	}
	e.emitLinks()
	return e.nodes, e.edges
}

// emitter carries shared state across per-record emission: the node/edge
// accumulators plus the page URL used to derive stable IDs.
type emitter struct {
	page   *pageRecord
	pageID string
	nodes  []*knowledgev1.Node
	edges  []kgwire.BatchEdge
}

func newEmitter(p *pageRecord) *emitter {
	return &emitter{
		page:   p,
		pageID: stableID(p.URL, "page", "", 0),
	}
}

// emitPageNode appends the root page node with page-level metadata.
//
// Description is populated by flattening TopSections' paragraph + list-item
// text into a single body string capped at pageDescriptionCap. Without this,
// recipes translating page → pattern (azure-patterns, hohpe-eip, etc.) only
// see the bare title — the resulting practice nodes carry no body content,
// so BM25 and HNSW indexes have nothing to match query tokens against and
// the patterns silently drop out of search. The flattened body is the
// minimum semantic content needed for downstream search to function.
func (e *emitter) emitPageNode() {
	md := map[string]string{
		"url":          e.page.URL,
		"final_url":    e.page.FinalURL,
		"http_status":  strconv.Itoa(e.page.HTTPStatus),
		"content_hash": e.page.ContentHash,
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
		Description: flattenPageBody(e.page.TopSections, pageDescriptionCap),
		Source:      "web-collect",
		Metadata:    md,
	})
}

// pageDescriptionCap bounds the flattened page body so a long article doesn't
// blow past the rerank doc-text or BM25 field budgets when downstream
// recipes copy page.description into a pattern node. ~8KB covers a typical
// reference-doc page (Azure pattern pages average ~6KB of prose) without
// over-stuffing.
const pageDescriptionCap = 8000

// flattenPageBody walks sections in document order and concatenates
// paragraph + list-item text with section headings as separators, capped
// at maxLen. Keeps headings inline ("## Heading\nbody...") so the
// cross-encoder reads the structure as natural documentation. Code blocks,
// tables, images, and quotes are skipped — they're typically shape rather
// than the searchable text the body summary needs to surface.
func flattenPageBody(sections []*sectionRecord, maxLen int) string {
	if len(sections) == 0 {
		return ""
	}
	var b strings.Builder
	for _, sec := range sections {
		appendSectionBody(&b, sec, maxLen)
		if b.Len() >= maxLen {
			break
		}
	}
	out := strings.TrimSpace(b.String())
	if len(out) > maxLen {
		out = out[:maxLen]
	}
	return out
}

// appendSectionBody writes one section's heading + content to b, recursing
// into nested sections. Stops appending once b crosses maxLen so the caller
// loop's bound check has a tight upper bound.
func appendSectionBody(b *strings.Builder, sec *sectionRecord, maxLen int) {
	if sec == nil || b.Len() >= maxLen {
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
		if b.Len() >= maxLen {
			return
		}
		switch r := child.(type) {
		case paragraphRecord:
			b.WriteString(r.Text)
			b.WriteByte('\n')
		case listRecord:
			for _, item := range r.Items {
				if b.Len() >= maxLen {
					return
				}
				b.WriteString("- ")
				b.WriteString(item.Text)
				b.WriteByte('\n')
			}
		case nestedSectionRecord:
			appendSectionBody(b, r.Section, maxLen)
		}
	}
}

// emitSection emits a section node plus every child record in document
// order. path threads through deterministic ID derivation so sibling
// sections at the same position don't collide.
func (e *emitter) emitSection(parentID, path string, sec *sectionRecord, idx int) {
	if sec == nil {
		return
	}
	myPath := extendPath(path, "section", idx)
	id := stableID(e.page.URL, "section", myPath, idx)
	md := map[string]string{
		"heading": sec.Heading,
		"depth":   strconv.Itoa(sec.Depth),
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
		e.emitContent(id, myPath, child, i)
	}
}

// emitContent dispatches a contentRecord to its kind-specific helper.
func (e *emitter) emitContent(parentID, path string, rec contentRecord, idx int) {
	switch r := rec.(type) {
	case nestedSectionRecord:
		e.emitSection(parentID, path, r.Section, idx)
	case paragraphRecord:
		e.emitParagraph(parentID, path, r, idx)
	case codeBlockRecord:
		e.emitCodeBlock(parentID, path, r, idx)
	case listRecord:
		e.emitList(parentID, path, r, idx)
	case tableRecord:
		e.emitTable(parentID, path, r, idx)
	case linkRecord:
		e.emitLink(parentID, path, r, idx)
	case imageRecord:
		e.emitImage(parentID, path, r, idx)
	case quoteRecord:
		e.emitQuote(parentID, path, r, idx)
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
