// SPDX-License-Identifier: Apache-2.0

package pdfcollector

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
)

// collectorSchemaVersion identifies the SHAPE this pdf collector emits — which
// node types it produces, which fields carry what, and which metadata keys it
// stamps. It is stamped on the document root under `collector_schema_version`
// and is BUMPED in the same change as any alteration to what this collector
// emits.
//
// It is an independent counter from the web collector's constant of the same
// name: the two shapes change on their own schedules, and a shared counter
// would force a false bump on the collector that did not change.
//
// An absent collector_schema_version on a collected graph means the graph
// predates versioning, and nothing can be concluded about its shape.
//
// Version 2 moved every raw node's searchable text into Content — the
// section heading and the document root's blurb both — and added the
// per-block raw layout signals plus the retained-chrome keys to node
// metadata. A version-1 graph carries neither, so a consumer reading
// Content or a signal key on one sees an empty value rather than a
// wrong one.
//
// VERSION 2 ALSO FLIPPED page_first AND page_last FROM ZERO-INDEXED TO
// ONE-INDEXED, and this is the version's only change a consumer cannot detect
// by looking: the keys are present on both shapes and both readings are
// plausible page numbers. A version-1 graph's first page cites 0 and a
// version-2 graph's cites 1 for the same page, so the two disagree by one on
// every citation. A consumer comparing citations across a re-collect reads
// that as a data discrepancy unless it reads the stamp.
//
// Version 3 changed the ORDER the tagged path emits for a page carrying a
// non-zero /Rotate. Cross-element reading order — how a page's tagged
// structure elements are merged with its untagged residue — is now keyed on a
// reading anchor transformed by the page rotation, where before it was keyed
// on raw page-space coordinates with no rotation term at all. No node type,
// field or metadata key changed; only the sequence moved.
//
// A version-2 graph of a ROTATED, partially-tagged document therefore holds a
// different node order, and different contains-edge positions, than a
// version-3 graph of byte-identical source. That is stated so a consumer can
// tell the two shapes apart. It is not a route from one to the other: no code
// converges a version-2 graph in place and no code drops one, so bringing an
// already-collected raw pdf graph onto this shape is an operator action.
//
// Unrotated documents are unaffected, and not merely in practice — the
// rotation transform is the identity at /Rotate 0, so a version-2 and a
// version-3 collection of an unrotated document agree by construction.
//
// VERSION 3 ALSO CHANGED WHAT THE DOCUMENT ROOT IS LABELED WITH. SymbolName is
// no longer the Info-dict Title verbatim: it is that Title when the Info dict
// carries one, else the first top-level section heading, else the file
// basename with its extension removed. The root carries a new key,
// title_source, valued info_dict, first_heading or filename — on every
// version-3 root, on no earlier one. The Content blurb opens on that same
// derived title. metadata.title is UNCHANGED and still reports only what the
// Info dictionary said, so a real Info-dict title stays distinguishable from a
// derived one; title_source names which it got.
//
// A version-2 graph of a TITLELESS document therefore holds an empty
// SymbolName and no title_source for the same source bytes. As with the
// ordering change above, nothing converges an already-collected graph:
// re-collecting is an operator action. Two shape changes share version 3
// because neither has shipped, so no version tells a rotation-only collect
// from a title-derived one.
const collectorSchemaVersion = 3

// The THREE values metadata.title_source may carry, declared here and nowhere
// else. Every stamping site cites one: a bare literal at the stamp is a second
// declaration, free to drift from this block without the compiler noticing.
const (
	titleSourceInfoDict     = "info_dict"
	titleSourceFirstHeading = "first_heading"
	titleSourceFilename     = "filename"
)

// deriveDocumentTitle answers what a reader is shown as this document's title
// and reports WHICH source answered: the Info dictionary's Title, else the
// first top-level section heading, else the basename with its extension
// removed. fileStem is total, so no leg returns "".
//
// THE GUARD TRIMS BUT THE RETURN DOES NOT: a real Info-dict title survives
// byte-identically, while a whitespace-only one falls through instead of
// rendering as a blank label. metadata.title records what the Info dict
// literally said; this decides what a reader is shown.
//
// TOP-LEVEL MEANS DIRECT CHILDREN OF THE ROOT — the loop never recurses into
// Children. The untagged path's top-level chunk is a text-less wrapper holding
// every classified block, so recursing would promote an arbitrary mid-document
// heading to the document's title.
func deriveDocumentTitle(meta pdf.Metadata, pdfPath string, chunks []pdf.Chunk) (string, string) {
	if strings.TrimSpace(meta.Title) != "" {
		return meta.Title, titleSourceInfoDict
	}
	for _, c := range chunks {
		if nodeTypeForChunk(c) != "section" {
			continue
		}
		if heading := strings.TrimSpace(c.Text); heading != "" {
			return heading, titleSourceFirstHeading
		}
	}
	return fileStem(pdfPath), titleSourceFilename
}

// fileStem returns path's final element with its extension removed, trimmed.
// IT NEVER SLUG-IFIES: no lowercasing, no substitution, no hash suffix — this
// is a value a human reads as a title, not a graph name, which is why
// SourceSlug and sanitizeSlug are wrong here. The second arm makes it total: a
// final element that is all extension ("/.pdf") yields the full base, not "".
func fileStem(path string) string {
	base := filepath.Base(path)
	if stem := strings.TrimSpace(strings.TrimSuffix(base, filepath.Ext(base))); stem != "" {
		return stem
	}
	return strings.TrimSpace(base)
}

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
//
// Every chunk node carries a `position` metadata key
// equal to its CONTAINS edge position; the document root, having no
// parent, carries none.
//
// chunk.Chunk.PageRange is zero-indexed and stays that way; page_first and
// page_last are emitted ONE-INDEXED, the same conversion pdf.Page.Number()
// applies at collector/pdf/page.go:66-71, and this emitter is the only place
// it happens.
//
// FIELD RULE: EVERY raw node's searchable text lands in Content. A LEAF
// node's body text is its Content; a SECTION node's Content is its
// heading, which is the only text a section has of its own (its body is
// its children); and the DOCUMENT ROOT's Content is the blurb, opening on
// the DERIVED title and continuing with the Info-dict author and subject.
// SymbolName additionally carries the heading on a section and that same
// derived title on the root, because the read surface labels a search hit
// with SymbolName and a section hit would otherwise render label-less.
// metadata.title_source reports which source the root's title came from,
// and metadata.title stays the Info-dict record rather than the derived
// value. This emitter writes Description on no node at all.
//
// The rule used to route a section's heading to SymbolName only and the
// root's blurb to Description, which meant "where is this node's text?"
// had three answers depending on the node kind, and a consumer reading
// Content saw a section as empty.
//
// A chunk-supplied `language` key reaches metadata.language through the
// generic per-chunk metadata copy; no pdf pipeline stage sets one today, so
// the key is absent on real collects and no default is ever invented.
//
// collectedAt is the instant the collect run started, captured by Collect and
// stamped on the document root. It is distinct from the PDF's own
// CreationDate and ModDate, which describe the document rather than the
// collect. A zero collectedAt writes no stamp at all.
// THE EMISSION IS REFUSED WHOLE ON A LOUD FAILURE. emit returns an error when
// any edge's evidence failed to marshal, and returns NO nodes and NO edges with
// it. A partial emission is the outcome this refusal exists to prevent: the
// server treats a pdf collect as the document's authoritative set and retires
// whatever the collect did not re-emit, so shipping the survivors of a failed
// build would delete the rest of the graph rather than leave it alone.
func emit(meta pdf.Metadata, pdfPath string, chunks []pdf.Chunk, collectedAt time.Time) ([]*knowledgev1.Node, []kgwire.BatchEdge, error) {
	e := newPDFEmitter(pdfPath, collectedAt)
	title, titleSource := deriveDocumentTitle(meta, pdfPath, chunks)
	e.emitDocumentNode(meta, title, titleSource)
	for i, c := range chunks {
		e.emitChunk(e.docID, "", c, i)
	}
	return e.result()
}

// result closes the emission: it hands back the accumulated nodes and edges, or
// — if any loud failure landed — the joined failure and NOTHING ELSE.
//
// IT IS A METHOD RATHER THAN THE TAIL OF emit so the refusal is reachable from
// a test. The branch it guards cannot be produced through the public entry
// point in a correct build (see jsonMeta), so a test driving emit end to end
// can only ever observe the success arm; driving this one with a seeded failure
// is what pins "a failed build emits nothing" rather than leaving it to a
// reader's confidence.
func (e *pdfEmitter) result() ([]*knowledgev1.Node, []kgwire.BatchEdge, error) {
	if len(e.errs) > 0 {
		return nil, nil, fmt.Errorf("emit pdf %s: %w", e.pdfPath, errors.Join(e.errs...))
	}
	return e.nodes, e.edges, nil
}

// pdfEmitter carries shared state across per-chunk emission: the
// node/edge accumulators and the absolute path used to derive stable
// IDs.
type pdfEmitter struct {
	pdfPath string
	docID   string
	// collectedAt is the collect-run instant stamped on the document root.
	// Zero means "not supplied" and suppresses the stamp.
	collectedAt time.Time
	nodes       []*knowledgev1.Node
	edges       []kgwire.BatchEdge
	// errs accumulates the LOUD failures of conditions that cannot occur in a
	// correct build — today the one marshal branch that used to drop an edge's
	// evidence silently. They accumulate rather than short-circuiting so one
	// collect reports every such failure it met instead of the first, and emit
	// refuses the whole document if any landed.
	errs []error
}

// fail records a loud failure on the emitter. Its caller is a branch a correct
// build never reaches; recording rather than returning keeps the per-chunk emit
// helpers free of an error return each, which would be an error path threaded
// through the whole recursive walk to carry a condition it cannot produce.
//
// This is the web emitter's accumulator, spelled the same way on purpose:
// collector/web/emit_nodes.go declares the identical field and method, and the
// two collectors reading differently on the same class of failure is the drift
// this mirroring exists to prevent.
func (e *pdfEmitter) fail(err error) {
	if err != nil {
		e.errs = append(e.errs, err)
	}
}

func newPDFEmitter(pdfPath string, collectedAt time.Time) *pdfEmitter {
	return &pdfEmitter{
		pdfPath:     pdfPath,
		docID:       stableID(pdfPath, "document", "", 0),
		collectedAt: collectedAt,
	}
}

// emitDocumentNode appends the root document node with PDF Info-dict
// metadata. Content carries a flattened concatenation of metadata
// fields so BM25 indexes downstream of the recipe transformer have at
// least the title + author + subject to match against. It is a blurb,
// never a flatten of the document's body: pages are made of chunks and
// the chunks are the nodes, so the root has no body of its own.
//
// title and titleSource come from deriveDocumentTitle: title labels the root
// in SymbolName and at the head of the blurb, and titleSource reports which
// source supplied it, unconditionally. md["title"] IS NOT THAT VALUE — it
// stays the Info dictionary's own record, still on a bare non-empty guard,
// because copying a derived title there would fabricate a document property
// no document stated.
func (e *pdfEmitter) emitDocumentNode(meta pdf.Metadata, title, titleSource string) {
	md := map[string]string{
		"source":                   "pdf",
		"path":                     e.pdfPath,
		"collector_schema_version": strconv.Itoa(collectorSchemaVersion),
		"title_source":             titleSource,
	}
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
	// collected_at records WHEN THE COLLECT RAN; creation_date and mod_date
	// above are the PDF's own dates and describe the document, not the
	// collect. A graph collected before this key existed renders as
	// unstamped rather than as the zero time, so absence stays absent.
	if !e.collectedAt.IsZero() {
		md["collected_at"] = e.collectedAt.UTC().Format(time.RFC3339)
	}
	e.nodes = append(e.nodes, &knowledgev1.Node{
		Id:         e.docID,
		Type:       "document",
		SymbolName: title,
		Content:    buildDocumentBlurb(meta, title),
		Source:     "pdf-collect",
		Metadata:   md,
	})
}

// buildDocumentBlurb concatenates the high-signal Info-dict fields into
// a single string so downstream BM25 indexes have text to match
// against. Empty fields are skipped.
//
// It opens on title — deriveDocumentTitle's answer — so a titleless document's
// blurb is labeled rather than starting at the author. The author and subject
// branches read the Info dict directly and are unchanged.
func buildDocumentBlurb(meta pdf.Metadata, title string) string {
	var b strings.Builder
	if title != "" {
		b.WriteString(title)
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
		"position":   strconv.Itoa(idx),
		"page_first": strconv.Itoa(c.PageRange[0] + 1),
		"page_last":  strconv.Itoa(c.PageRange[1] + 1),
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
	content := c.Text
	if kind == "section" {
		// A section's body is its children, so its own text is its
		// heading. That text goes in Content like every other raw node's
		// does, and ALSO in SymbolName, which is the label the read
		// surface renders a search hit with.
		symbol = strings.TrimSpace(c.Text)
		content = symbol
	}

	e.nodes = append(e.nodes, &knowledgev1.Node{
		Id:         id,
		Type:       kind,
		SymbolName: symbol,
		Content:    content,
		Source:     "pdf-collect",
		Metadata:   md,
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
//
// A FAILED EVIDENCE MARSHAL NAMES THE EDGE AND FAILS THE EMIT. `position` is
// the only thing reconstructing document order, so an edge that lost it is not
// a lesser edge — it is an edge whose place in the document is gone, and it is
// byte-indistinguishable from an edge that legitimately carried no evidence.
func (e *pdfEmitter) addContains(parentID, childID string, pos int) {
	evidence, err := jsonMeta(map[string]string{"position": strconv.Itoa(pos)})
	if err != nil {
		e.fail(fmt.Errorf("contains edge %s -> %s at position %d: %w", parentID, childID, pos, err))
	}
	e.edges = append(e.edges, kgwire.BatchEdge{
		FromIdx:  -1,
		ToIdx:    -1,
		FromID:   parentID,
		ToID:     childID,
		Type:     kgtypes.EdgeContains,
		Method:   "pdf-collect",
		Evidence: evidence,
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

// jsonMeta marshals a small string map into JSON for edge Evidence storage.
//
// A MARSHAL FAILURE IS RETURNED, NOT ABSORBED INTO AN EMPTY STRING. The
// previous form returned "" on error, which is byte-identical to the empty map
// case — so a failure lost an edge's position with no way for any caller to
// tell that from an edge that legitimately carried no evidence, and no log
// either. It cannot fail for a string map in a correct build, which is
// precisely why the branch is an error rather than a fallback.
//
// This is collector/web/emit_nodes.go's jsonMeta, signature and reasoning
// alike. The web twin was made loud and this one was not, in the same change;
// two collectors disagreeing about whether a lost edge position is worth
// reporting is the divergence that mirroring closes.
func jsonMeta(m map[string]string) (string, error) {
	if len(m) == 0 {
		return "", nil
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return "", fmt.Errorf("marshal edge evidence: %w", err)
	}
	return string(raw), nil
}
