// SPDX-License-Identifier: Apache-2.0

package pdfcollector

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// emit translates a flat slice of top-level Chunks into *knowledgev1.Node and
// kgwire.BatchEdge slices ready for the create_batch wire. The root node
// is a "document" carrying PDF Info-dict metadata; every Chunk becomes
// a child node connected by an EdgeContains edge with a zero-based
// position on Evidence so downstream consumers can reconstruct
// document order.
//
// Bare-name node vocabulary mirrors collector/web's emitter so recipes
// can stay source-agnostic: heading chunks land as "section", paragraph
// chunks as "paragraph", code chunks as "code_block", list-items as
// "list_item", tables as "table", and any unclassified block as
// "block". Every emitted node carries Metadata["source"] = "pdf".
//
// IDs are deterministic: sha256(absolutePath || kind || path || idx)
// truncated to 16 hex chars. Re-running emit() on the same input yields
// identical IDs, so callers can dedupe across invocations.
func emit(meta pdf.Metadata, pdfPath string, chunks []pdf.Chunk) ([]*knowledgev1.Node, []kgwire.BatchEdge) {
	e := newPDFEmitter(pdfPath)
	e.emitDocumentNode(meta)
	for i, c := range chunks {
		e.emitChunk(e.docID, "", c, i)
	}
	return e.nodes, e.edges
}

// pdfEmitter carries shared state across per-chunk emission: the
// node/edge accumulators and the absolute path used to derive stable
// IDs.
type pdfEmitter struct {
	pdfPath string
	docID   string
	nodes   []*knowledgev1.Node
	edges   []kgwire.BatchEdge
}

func newPDFEmitter(pdfPath string) *pdfEmitter {
	return &pdfEmitter{
		pdfPath: pdfPath,
		docID:   stableID(pdfPath, "document", "", 0),
	}
}

// emitDocumentNode appends the root document node with PDF Info-dict
// metadata. Description carries a flattened concatenation of metadata
// fields so BM25 indexes downstream of the recipe transformer (which
// copies document.description into its synthesized pattern node) have
// at least the title + author + subject to match against.
func (e *pdfEmitter) emitDocumentNode(meta pdf.Metadata) {
	md := map[string]string{"source": "pdf", "path": e.pdfPath}
	if meta.Title != "" {
		md["title"] = meta.Title
	}
	if meta.Author != "" {
		md["author"] = meta.Author
	}
	if meta.Subject != "" {
		md["subject"] = meta.Subject
	}
	if meta.Keywords != "" {
		md["keywords"] = meta.Keywords
	}
	if meta.Producer != "" {
		md["producer"] = meta.Producer
	}
	if meta.Creator != "" {
		md["creator"] = meta.Creator
	}
	if !meta.CreationDate.IsZero() {
		md["creation_date"] = meta.CreationDate.UTC().Format("2006-01-02T15:04:05Z07:00")
	}
	if !meta.ModDate.IsZero() {
		md["mod_date"] = meta.ModDate.UTC().Format("2006-01-02T15:04:05Z07:00")
	}
	desc := buildDocumentDescription(meta)
	e.nodes = append(e.nodes, &knowledgev1.Node{
		Id:          e.docID,
		Type:        "document",
		SymbolName:  meta.Title,
		Description: desc,
		Source:      "pdf-collect",
		Metadata:    md,
	})
}

// buildDocumentDescription concatenates the high-signal metadata
// fields into a single string so downstream BM25 indexes have body
// text to match against. Empty fields are skipped.
func buildDocumentDescription(meta pdf.Metadata) string {
	var b strings.Builder
	if meta.Title != "" {
		b.WriteString(meta.Title)
	}
	if meta.Author != "" {
		if b.Len() > 0 {
			b.WriteString(" — ")
		}
		b.WriteString(meta.Author)
	}
	if meta.Subject != "" {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(meta.Subject)
	}
	return b.String()
}

// emitChunk appends a node for c and recurses into Children. parentID
// receives an EdgeContains edge with the position on Evidence. path
// threads the kind+idx ancestry into stable-ID derivation so sibling
// chunks at the same index do not collide.
func (e *pdfEmitter) emitChunk(parentID, path string, c pdf.Chunk, idx int) {
	kind := nodeTypeForChunk(c)
	myPath := extendPath(path, kind, idx)
	id := stableID(e.pdfPath, kind, myPath, idx)

	md := map[string]string{
		"source":     "pdf",
		"page_first": strconv.Itoa(c.PageRange[0]),
		"page_last":  strconv.Itoa(c.PageRange[1]),
	}
	if c.HeadingLevel > 0 {
		md["heading_level"] = strconv.Itoa(c.HeadingLevel)
	}
	if c.StructRole != "" {
		md["struct_role"] = c.StructRole
	}
	if c.Kind != "" {
		md["chunk_kind"] = string(c.Kind)
	}
	for k, v := range c.Metadata {
		// Don't allow per-chunk metadata to clobber our reserved keys.
		if _, reserved := md[k]; reserved {
			continue
		}
		md[k] = v
	}

	symbol := ""
	if kind == "section" {
		symbol = strings.TrimSpace(c.Text)
	}

	e.nodes = append(e.nodes, &knowledgev1.Node{
		Id:          id,
		Type:        kind,
		SymbolName:  symbol,
		Description: c.Text,
		Source:      "pdf-collect",
		Metadata:    md,
	})
	e.addContains(parentID, id, idx)

	for i, child := range c.Children {
		e.emitChunk(id, myPath, child, i)
	}
}

// nodeTypeForChunk maps the chunker's BlockKind onto the bare-name
// node-type vocabulary recipes expect. Heading chunks become
// "section" so the document → section → paragraph nesting mirrors the
// web emitter's shape.
func nodeTypeForChunk(c pdf.Chunk) string {
	switch c.Kind {
	case pdf.BlockHeading:
		return "section"
	case pdf.BlockParagraph:
		return "paragraph"
	case pdf.BlockCode:
		return "code_block"
	case pdf.BlockListItem:
		return "list_item"
	case pdf.BlockTable:
		return "table"
	default:
		return "block"
	}
}

// addContains appends a parent→child EdgeContains edge with a
// zero-based `position` on Evidence so downstream consumers can
// reconstruct document order.
func (e *pdfEmitter) addContains(parentID, childID string, pos int) {
	e.edges = append(e.edges, kgwire.BatchEdge{
		FromIdx:  -1,
		ToIdx:    -1,
		FromID:   parentID,
		ToID:     childID,
		Type:     kgtypes.EdgeContains,
		Method:   "pdf-collect",
		Evidence: jsonMeta(map[string]string{"position": strconv.Itoa(pos)}),
	})
}

// stableID returns a 16-hex-char deterministic identifier derived from
// the absolute PDF path, chunk kind, structural path, and sibling
// index. Mirrors collector/web's stableID shape.
func stableID(pdfPath, kind, path string, idx int) string {
	sum := sha256.Sum256([]byte(pdfPath + "|" + kind + "|" + path + "|" + strconv.Itoa(idx)))
	return hex.EncodeToString(sum[:8])
}

// extendPath appends a kind/idx segment to path so nested emission
// yields unique IDs even when two sibling chunks of different kinds
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
