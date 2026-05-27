// SPDX-License-Identifier: Apache-2.0

package web

import (
	"encoding/json"
	"log/slog"
	"strconv"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// emitParagraph emits one "paragraph" node under parentID.
func (e *emitter) emitParagraph(parentID, path string, r paragraphRecord, idx int) {
	myPath := extendPath(path, "paragraph", idx)
	id := stableID(e.page.URL, "paragraph", myPath, idx)
	md := map[string]string{"position": strconv.Itoa(idx)}
	applyCommonAttrs(md, r.Attrs)
	applyInlineEmphasis(md, r.InlineEmphasis)
	e.nodes = append(e.nodes, &knowledgev1.Node{
		Id:       id,
		Type:     r.recordKind(),
		Content:  r.Text,
		Source:   "web-collect",
		Metadata: md,
	})
	e.addContains(parentID, id, idx)
}

// applyInlineEmphasis JSON-encodes the emphasis list onto md under the
// stable "inline_emphasis" key when the list is non-empty. Marshal
// errors are logged at debug and the key is omitted — a simple slice of
// {tag,text,position} cannot realistically fail to marshal, but the
// branch is explicit so the omission is not silent (per the nilerr rule).
func applyInlineEmphasis(md map[string]string, emphs []inlineEmphasis) {
	if len(emphs) == 0 {
		return
	}
	raw, err := json.Marshal(emphs)
	if err != nil {
		slog.Debug("web.emit: inline_emphasis marshal failed, omitting key", "err", err)
		return
	}
	md["inline_emphasis"] = string(raw)
}

// emitCodeBlock emits one "code_block" node under parentID. Language is
// surfaced as metadata and also on the Node.Language scalar.
func (e *emitter) emitCodeBlock(parentID, path string, r codeBlockRecord, idx int) {
	myPath := extendPath(path, "code_block", idx)
	id := stableID(e.page.URL, "code_block", myPath, idx)
	md := map[string]string{"position": strconv.Itoa(idx)}
	if r.Language != "" {
		md["language"] = r.Language
	}
	if r.AttrHint != "" {
		md["attr_hint"] = r.AttrHint
	}
	applyCommonAttrs(md, r.Attrs)
	e.nodes = append(e.nodes, &knowledgev1.Node{
		Id:       id,
		Type:     r.recordKind(),
		Language: r.Language,
		Content:  r.Source,
		Source:   "web-collect",
		Metadata: md,
	})
	e.addContains(parentID, id, idx)
}

// emitList emits a "list" node plus one "list_item" node per entry,
// each list_item contained by the list with a position on its edge.
func (e *emitter) emitList(parentID, path string, r listRecord, idx int) {
	myPath := extendPath(path, "list", idx)
	listID := stableID(e.page.URL, "list", myPath, idx)
	md := map[string]string{
		"position": strconv.Itoa(idx),
		"kind":     r.Kind,
		"ordered":  strconv.FormatBool(r.Ordered),
	}
	applyCommonAttrs(md, r.Attrs)
	e.nodes = append(e.nodes, &knowledgev1.Node{
		Id:       listID,
		Type:     r.recordKind(),
		Source:   "web-collect",
		Metadata: md,
	})
	e.addContains(parentID, listID, idx)

	for _, item := range r.Items {
		itemPath := extendPath(myPath, item.recordKind(), item.Position)
		itemID := stableID(e.page.URL, item.recordKind(), itemPath, item.Position)
		itemMd := map[string]string{"position": strconv.Itoa(item.Position)}
		applyCommonAttrs(itemMd, item.Attrs)
		applyInlineEmphasis(itemMd, item.InlineEmphasis)
		e.nodes = append(e.nodes, &knowledgev1.Node{
			Id:       itemID,
			Type:     item.recordKind(),
			Content:  item.Text,
			Source:   "web-collect",
			Metadata: itemMd,
		})
		e.addContains(listID, itemID, item.Position)
	}
}

// emitTable emits a "table" node carrying headers/rows as JSON on
// content metadata. Rows and headers stay inline rather than becoming
// individual nodes because downstream consumers typically want the
// whole table as a unit.
func (e *emitter) emitTable(parentID, path string, r tableRecord, idx int) {
	myPath := extendPath(path, "table", idx)
	id := stableID(e.page.URL, "table", myPath, idx)
	md := map[string]string{"position": strconv.Itoa(idx)}
	if h, err := json.Marshal(r.Headers); err == nil {
		md["headers"] = string(h)
	}
	if rows, err := json.Marshal(r.Rows); err == nil {
		md["rows"] = string(rows)
	}
	applyCommonAttrs(md, r.Attrs)
	e.nodes = append(e.nodes, &knowledgev1.Node{
		Id:       id,
		Type:     r.recordKind(),
		Source:   "web-collect",
		Metadata: md,
	})
	e.addContains(parentID, id, idx)
}

// emitLink emits a "link" node under parentID. The link is also
// registered in e.page.InternalLinks / e.page.ExternalCites already,
// so emitLinks() handles page-level reference edges separately. This
// per-section link node captures the in-section anchor.
func (e *emitter) emitLink(parentID, path string, r linkRecord, idx int) {
	myPath := extendPath(path, "link", idx)
	id := stableID(e.page.URL, "link", myPath, idx)
	md := map[string]string{
		"position": strconv.Itoa(idx),
		"url":      r.URL,
		"rel":      r.Rel,
	}
	if r.Anchor != "" {
		md["anchor"] = r.Anchor
	}
	if r.NoFollow {
		md["nofollow"] = "true"
	}
	applyCommonAttrs(md, r.Attrs)
	e.nodes = append(e.nodes, &knowledgev1.Node{
		Id:         id,
		Type:       r.recordKind(),
		SymbolName: r.Text,
		Source:     "web-collect",
		Metadata:   md,
	})
	e.addContains(parentID, id, idx)
}

// emitImage emits an "image" node.
func (e *emitter) emitImage(parentID, path string, r imageRecord, idx int) {
	myPath := extendPath(path, "image", idx)
	id := stableID(e.page.URL, "image", myPath, idx)
	md := map[string]string{
		"position": strconv.Itoa(idx),
		"url":      r.URL,
	}
	if r.Alt != "" {
		md["alt"] = r.Alt
	}
	if r.Caption != "" {
		md["caption"] = r.Caption
	}
	applyCommonAttrs(md, r.Attrs)
	e.nodes = append(e.nodes, &knowledgev1.Node{
		Id:       id,
		Type:     r.recordKind(),
		Source:   "web-collect",
		Metadata: md,
	})
	e.addContains(parentID, id, idx)
}

// emitQuote emits a "blockquote" node.
func (e *emitter) emitQuote(parentID, path string, r quoteRecord, idx int) {
	myPath := extendPath(path, "blockquote", idx)
	id := stableID(e.page.URL, "blockquote", myPath, idx)
	md := map[string]string{"position": strconv.Itoa(idx)}
	if r.CiteURL != "" {
		md["cite_url"] = r.CiteURL
	}
	applyCommonAttrs(md, r.Attrs)
	applyInlineEmphasis(md, r.InlineEmphasis)
	e.nodes = append(e.nodes, &knowledgev1.Node{
		Id:       id,
		Type:     r.recordKind(),
		Content:  r.Text,
		Source:   "web-collect",
		Metadata: md,
	})
	e.addContains(parentID, id, idx)
}
