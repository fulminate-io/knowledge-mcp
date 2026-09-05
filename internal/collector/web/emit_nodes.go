// SPDX-License-Identifier: Apache-2.0

package web

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
)

// collectorSchemaVersion identifies the SHAPE this web collector emits — which
// node types it produces, which fields carry what, and which metadata keys it
// stamps. It is stamped on the page root under `collector_schema_version` and
// is BUMPED in the same change as any alteration to what this collector emits.
//
// It is an independent counter from the pdf collector's constant of the same
// name: the two shapes change on their own schedules, and a shared counter
// would force a false bump on the collector that did not change.
//
// An absent collector_schema_version on a collected graph means the graph
// predates versioning, and nothing can be concluded about its shape.
//
// 3 — the page root additionally records the crawl's SOURCE IDENTITY:
// source_name is the graph the crawl landed under and seed_host is the
// normalized host of its first seed URL. seed_host is the value the
// collect-time collision refusal compares an incoming crawl against.
// 2 — every node carries its source tag and DOM depth, the prose kinds carry a
// rune text_length, sections carry the arm that opened them plus the heuristic
// arm's calibration inputs, tables carry the layout verdict with the four
// measurements behind it, inline runs carry attribute provenance and are
// retained when they hold nothing but links, and THE PAGE NODE CARRIES NO
// BODY — a page is its chunks.
// 1 — the shape before that.
const collectorSchemaVersion = 3

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
//
// collectedAt is the instant the WHOLE collect run started, captured once by
// Collect and passed down unchanged, so every page root of one crawl records
// the same collect time. It is not the page's own FetchedAt, which stays
// per-page. A zero collectedAt writes no stamp at all.
func emitFromPage(p *pageRecord, collectedAt time.Time, src graphSource) ([]*knowledgev1.Node, []kgwire.BatchEdge, error) {
	if p == nil {
		return nil, nil, nil
	}
	e := newEmitter(p, collectedAt, src)
	e.emitPageNode()
	for i, sec := range p.TopSections {
		e.emitSection(e.pageID, "", sec, i, e.pageURI)
	}
	// Appended LAST, at position len(TopSections), so the retained-HTML node
	// arrives without shifting any section's contains-position.
	e.emitRawHTML(len(p.TopSections))
	e.emitLinks()
	if len(e.errs) > 0 {
		return nil, nil, fmt.Errorf("emit page %s: %w", p.URL, errors.Join(e.errs...))
	}
	return e.nodes, e.edges, nil
}

// graphSource is the CRAWL-LEVEL PROVENANCE of one collect: the graph name the
// run landed under and the normalized host of its first seed URL. It is
// computed ONCE per collect from the same production derivation that named the
// graph, so the source a page root RECORDS can never disagree with the name the
// graph actually landed under — which is the whole basis on which the
// collect-time collision refusal decides whether a later crawl is the same site.
//
// A zero value writes no keys, which is how a caller that has no provenance to
// state says so.
type graphSource struct {
	Name     string
	SeedHost string
}

// emitter carries shared state across per-record emission: the node/edge
// accumulators, the page URL used to derive stable IDs, and pageURI — the
// address the content was served from after redirects, which every emitted
// node carries under the `uri` metadata key.
type emitter struct {
	page    *pageRecord
	pageID  string
	pageURI string
	// collectedAt is the run-wide collect instant, shared by every page of
	// one crawl. Zero means "not supplied" and suppresses the stamp.
	collectedAt time.Time
	// src is the crawl's source identity. It rides down for the same reason
	// collectedAt does: it is a fact about the RUN, not about any page, so
	// every page root of one crawl must agree on it rather than each deriving
	// its own from whatever URL it happens to hold.
	src   graphSource
	nodes []*knowledgev1.Node
	edges []kgwire.BatchEdge
	// errs accumulates the LOUD failures of conditions that cannot occur in a
	// correct build — the four marshal branches that used to drop a metadata
	// key or an edge's evidence silently. They accumulate rather than
	// short-circuiting so one collect reports every such failure it met
	// instead of the first, and emitFromPage refuses the whole page if any
	// landed.
	errs []error
}

// fail records a loud failure on the emitter. Every caller is a branch that a
// correct build never reaches; recording rather than returning keeps the
// per-record emit helpers free of an error return each, which would be an
// error path on every one of eleven emitters to carry a condition none of them
// can produce.
func (e *emitter) fail(err error) {
	if err != nil {
		e.errs = append(e.errs, err)
	}
}

func newEmitter(p *pageRecord, collectedAt time.Time, src graphSource) *emitter {
	return &emitter{
		page:        p,
		pageID:      stableID(p.URL, "page", "", 0),
		pageURI:     p.FinalURL,
		collectedAt: collectedAt,
		src:         src,
	}
}

// emitPageNode appends the root page node with page-level metadata.
//
// THE PAGE NODE CARRIES NO BODY, in Content or in Description, under the
// user's ruling: "pages are made up of chunks never put the whole page as
// content". The page-level flatten that used to compose one is RETIRED rather
// than relocated to the other field.
//
// WHY, in a sentence a reader can check against the code below: every
// paragraph, code block, list item, table and blockquote is already its own
// node carrying its own text, so a page-level flatten is a second copy of
// every word in the graph — and the copy is the one that goes stale. A
// consumer wanting the page's prose composes it from the children over
// CONTAINS.
//
// THE UNTRUNCATED GUARANTEE THE RETIRED FLATTEN OWED IS NOW SATISFIED BY
// CONSTRUCTION, not dropped: a page body cannot be silently truncated because
// no page body is composed, and each chunk node carries its own untruncated
// text.
//
// WHAT IS REMOVED IS THE BODY AND ONLY THE BODY. Every metadata key stays,
// and so does the identity the page carries: SymbolName is the title, and
// url / final_url / http_status / content_hash / uri / the schema stamp /
// fetched_at / collected_at / the optional title, author and pub_date keys /
// the body element's attrs all remain.
func (e *emitter) emitPageNode() {
	md := map[string]string{
		"url":                      e.page.URL,
		"final_url":                e.page.FinalURL,
		"http_status":              strconv.Itoa(e.page.HTTPStatus),
		"content_hash":             e.page.ContentHash,
		"uri":                      e.pageURI,
		"collector_schema_version": strconv.Itoa(collectorSchemaVersion),
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
	// collected_at records WHEN THE COLLECT RAN, one value for the whole
	// crawl; fetched_at above records when THIS page was retrieved and keeps
	// its per-page meaning. A graph collected before this key existed renders
	// as unstamped rather than as the zero time, so absence stays absent.
	if !e.collectedAt.IsZero() {
		md["collected_at"] = e.collectedAt.UTC().Format(time.RFC3339)
	}
	// The crawl's source identity, one value for the whole run. Each is written
	// ONLY when non-empty, so a graph collected before these keys existed renders
	// as ABSENT rather than as an empty string — the same absence contract
	// collected_at above carries, and the one the collision refusal depends on:
	// an unrecorded source means nothing is known, never that it differs.
	if e.src.Name != "" {
		md["source_name"] = e.src.Name
	}
	if e.src.SeedHost != "" {
		md["seed_host"] = e.src.SeedHost
	}
	e.fail(applyCommonAttrs(md, e.page.Attrs))
	e.nodes = append(e.nodes, &knowledgev1.Node{
		Id:         e.pageID,
		Type:       "page",
		SymbolName: e.page.Title,
		Source:     "web-collect",
		Metadata:   md,
	})
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
//
// Every node this emitter contains under a parent carries a `position`
// metadata key equal to its CONTAINS edge position; the page root, having
// no parent, carries none.
//
// THE HEADING IS WRITTEN INTO Content AS WELL AS SymbolName. With the page
// flatten retired, the heading text exists on no other node, and Content is
// the field every content composer reads — a heading only in SymbolName would
// be text the document carried and the composed body silently omits. The
// synthetic depth-0 root section has no heading and so writes nothing, which
// is the correct reading of a section the document never declared.
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
		"heading":  sec.Heading,
		"depth":    strconv.Itoa(sec.Depth),
		"uri":      myURI,
		"position": strconv.Itoa(idx),
	}
	if sec.Anchor != "" {
		md["anchor"] = sec.Anchor
	}
	applyHeadingSignal(md, sec)
	e.fail(applyCommonAttrs(md, sec.Attrs))
	e.nodes = append(e.nodes, &knowledgev1.Node{
		Id:         id,
		Type:       "section",
		SymbolName: sec.Heading,
		Content:    sec.Heading,
		Source:     "web-collect",
		Metadata:   md,
	})
	e.addContains(parentID, id, idx)

	for i, child := range sec.Children {
		e.emitContent(id, myPath, child, i, myURI)
	}
}

// applyHeadingSignal stamps WHICH dispatch arm opened this section and, on the
// heuristic arm alone, the four measurements that produced its level.
//
// THE NEGATIVE DIRECTION IS THE POINT AND IT IS GATED. A native or aria
// section carries NO heuristic_* key, because it was never measured against
// anything — stamping the group key and the sibling median on a plain <h2>
// would describe a calibration that did not happen. HeuristicInputs is nil on
// exactly those arms, so the nil check IS the invariant rather than a
// defensive guard around it.
//
// The synthetic depth-0 root section was opened by no arm and carries no
// heading_source either; that absence says the section is the walker's own
// sink for pre-heading prose rather than anything the document declared.
//
// The sibling median is a float and is formatted WITHOUT AN EXPONENT
// (FormatFloat 'f' with -1 precision) so a recipe reading the key gets a plain
// decimal number: a group whose baseline came out as 1e+06 would otherwise
// stamp a string no numeric comparison in the DSL can read.
func applyHeadingSignal(md map[string]string, sec *sectionRecord) {
	if sec.HeadingSource != "" {
		md["heading_source"] = sec.HeadingSource
	}
	sig := sec.HeuristicInputs
	if sig == nil {
		return
	}
	md["heuristic_class_group"] = sig.classGroup
	md["heuristic_text_length"] = strconv.Itoa(sig.textLen)
	md["heuristic_sibling_median"] = strconv.FormatFloat(sig.siblingMedian, 'f', -1, 64)
	md["heuristic_group_size"] = strconv.Itoa(sig.groupSize)
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
		evidence, err := jsonMeta(map[string]string{"rel": "internal", "url": u})
		e.fail(err)
		e.edges = append(e.edges, kgwire.BatchEdge{
			FromIdx:  -1,
			ToIdx:    -1,
			FromID:   e.pageID,
			ToID:     "web:url:" + u,
			Type:     kgtypes.EdgeReferences,
			Method:   "web-collect",
			Evidence: evidence,
		})
	}
	for _, c := range e.page.ExternalCites {
		evidence, err := jsonMeta(map[string]string{"rel": "external", "url": c.URL})
		e.fail(err)
		e.edges = append(e.edges, kgwire.BatchEdge{
			FromIdx:  -1,
			ToIdx:    -1,
			FromID:   e.pageID,
			ToID:     "web:url:" + c.URL,
			Type:     kgtypes.EdgeReferences,
			Method:   "web-collect",
			Evidence: evidence,
		})
	}
}

// addContains appends a parent→child EdgeContains edge with a
// zero-based `position` on Evidence so downstream consumers can
// reconstruct document order.
func (e *emitter) addContains(parentID, childID string, pos int) {
	evidence, err := jsonMeta(map[string]string{"position": strconv.Itoa(pos)})
	e.fail(err)
	e.edges = append(e.edges, kgwire.BatchEdge{
		FromIdx:  -1,
		ToIdx:    -1,
		FromID:   parentID,
		ToID:     childID,
		Type:     kgtypes.EdgeContains,
		Method:   "web-collect",
		Evidence: evidence,
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

// jsonMeta marshals a small string map into JSON for edge Evidence storage.
//
// A MARSHAL FAILURE IS RETURNED, NOT ABSORBED INTO AN EMPTY STRING. The
// previous form returned "" on error, which is byte-identical to the empty map
// case — so a failure lost an edge's position or its rel with no way for any
// caller to tell that from an edge that legitimately carried no evidence. It
// cannot fail for a string map in a correct build, which is precisely why the
// branch is an error rather than a fallback.
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
