// SPDX-License-Identifier: Apache-2.0

package web

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// emitParagraph emits one "paragraph" node under parentID. uri is the
// enclosing section's address, stamped on the node. text_length is the
// record's own text measured in RUNES — see runeLen for why not bytes.
//
// A LINKS_ONLY RUN'S TEXT LANDS ON DESCRIPTION, NOT CONTENT, and the field is
// the locked half of the rule rather than a stylistic choice. Chrome is a
// signal, not a chunk: every content composer reads Content — with the
// page-level flatten retired, a subtree concatenation over CONTAINS/content IS
// the page-body path — so a navigation strip left in Content contaminates
// every composed body one package over, where no test in this package can see
// it. The node, its links_only signal, its tag, dom_depth, attrs and
// text_length are identical either way; only the field the text lands in
// differs, and text_length stays the rune count of the run's own text wherever
// it lives.
//
// The node keeps the `paragraph` TYPE, because the graph keeps its node
// vocabulary. Consumers that count prose by type therefore subtract it by
// name; see the substantive count in this collector's composition assertion.
func (e *emitter) emitParagraph(parentID, path string, r paragraphRecord, idx int, uri string) {
	myPath := extendPath(path, "paragraph", idx)
	id := stableID(e.page.URL, "paragraph", myPath, idx)
	md := map[string]string{
		"position":    strconv.Itoa(idx),
		"uri":         uri,
		"text_length": runeLen(r.Text),
	}
	if r.LinksOnly {
		md["links_only"] = "true"
	}
	e.fail(applyCommonAttrs(md, r.Attrs))
	e.fail(applyInlineEmphasis(md, r.InlineEmphasis))
	content, description := r.Text, ""
	if r.LinksOnly {
		content, description = "", r.Text
	}
	e.nodes = append(e.nodes, &knowledgev1.Node{
		Id:          id,
		Type:        r.recordKind(),
		Content:     content,
		Description: description,
		Source:      "web-collect",
		Metadata:    md,
	})
	e.addContains(parentID, id, idx)
}

// applyInlineEmphasis JSON-encodes the emphasis list onto md under the stable
// "inline_emphasis" key when the list is non-empty.
//
// A MARSHAL FAILURE IS RETURNED. It was previously logged at Debug and the key
// omitted, which is the exact shape this ticket exists to remove: a debug line
// nobody sees at default verbosity, a key silently missing, and a collect that
// reports success. A slice of {tag,text,position} cannot realistically fail to
// marshal, and that is the argument for making the branch loud rather than for
// making it lenient.
func applyInlineEmphasis(md map[string]string, emphs []inlineEmphasis) error {
	if len(emphs) == 0 {
		return nil
	}
	raw, err := json.Marshal(emphs)
	if err != nil {
		return fmt.Errorf("marshal inline_emphasis: %w", err)
	}
	md["inline_emphasis"] = string(raw)
	return nil
}

// emitCodeBlock emits one "code_block" node under parentID. Language is
// surfaced as metadata and also on the Node.Language scalar. uri is the
// enclosing section's address, stamped on the node.
func (e *emitter) emitCodeBlock(parentID, path string, r codeBlockRecord, idx int, uri string) {
	myPath := extendPath(path, "code_block", idx)
	id := stableID(e.page.URL, "code_block", myPath, idx)
	md := map[string]string{"position": strconv.Itoa(idx), "uri": uri}
	if r.Language != "" {
		md["language"] = r.Language
	}
	if r.AttrHint != "" {
		md["attr_hint"] = r.AttrHint
	}
	e.fail(applyCommonAttrs(md, r.Attrs))
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
// each list_item contained by the list with a position on its edge. uri is
// the enclosing section's address; the SAME value is stamped on the list
// node and on every list_item it emits.
//
// Each list_item carries text_length; the list node itself does not, because
// the list holds no text of its own — its items do.
//
// THE LIST'S VERDICT IS INHERITED BY EVERY ITEM under the key list_nav, so a
// consumer counting items can subtract navigation without first walking up to
// the list; the item also carries its own list_item_link_only measurement.
//
// list_nav is therefore WRITTEN AT TWO SITES — applyListSignals for the list
// node, and the item loop below — where table_layout is written at one. Both
// reads are of the SAME r.Signals.Nav, so there remains exactly one place the
// verdict is decided; only its stamping is duplicated.
//
// THE ITEM KEY IS DELIBERATELY NOT links_only. That key belongs to
// emitParagraph and carries a SECOND obligation — the text moves out of Content
// and into Description — which does not apply here. A NAV LIST ITEM STILL
// WRITES ITS TEXT INTO Content: a list item is a chunk of the document with a
// position in a list, not a strip recovered from prose flow, and relocating its
// text would change what every composed page body contains.
func (e *emitter) emitList(parentID, path string, r listRecord, idx int, uri string) {
	myPath := extendPath(path, "list", idx)
	listID := stableID(e.page.URL, "list", myPath, idx)
	md := map[string]string{
		"position": strconv.Itoa(idx),
		"kind":     r.Kind,
		"ordered":  strconv.FormatBool(r.Ordered),
		"uri":      uri,
	}
	e.fail(applyCommonAttrs(md, r.Attrs))
	applyListSignals(md, r.Signals)
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
		itemMd := map[string]string{
			"position":            strconv.Itoa(item.Position),
			"uri":                 uri,
			"text_length":         runeLen(item.Text),
			"list_nav":            strconv.FormatBool(r.Signals.Nav),
			"list_item_link_only": strconv.FormatBool(item.LinkOnly),
		}
		e.fail(applyCommonAttrs(itemMd, item.Attrs))
		e.fail(applyInlineEmphasis(itemMd, item.InlineEmphasis))
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
// whole table as a unit. uri is the enclosing section's address, stamped
// on the node.
//
// A DATA TABLE WRITES ITS CELL TEXT INTO Content; A LAYOUT TABLE WRITES NONE,
// and the split is the point rather than an optimization. A data table IS the
// chunk: its cells are emitted as no other node, so without this they exist in
// the graph only as JSON inside a metadata value, which no content composer
// reads. A layout table is a wrapper whose cells are walked into their own
// records, so writing them here too would put the same words on two nodes —
// the duplication the retired page flatten used to hide by skipping layout
// tables at flatten time. The layout node keeps its verdict, its measurements
// and its structured rows metadata; only the searchable text differs.
func (e *emitter) emitTable(parentID, path string, r tableRecord, idx int, uri string) {
	myPath := extendPath(path, "table", idx)
	id := stableID(e.page.URL, "table", myPath, idx)
	md := map[string]string{"position": strconv.Itoa(idx), "uri": uri}
	// LOUD, NOT LENIENT. These two used to omit their key when the marshal
	// errored, with no log at all, so a table reached the graph with its rows
	// missing and nothing to say they had ever existed. A []string and a
	// [][]string cannot fail to marshal in a correct build; the error is
	// propagated precisely because reaching it means the build is not correct.
	h, err := json.Marshal(r.Headers)
	e.fail(err)
	if err == nil {
		md["headers"] = string(h)
	}
	rows, err := json.Marshal(r.Rows)
	e.fail(err)
	if err == nil {
		md["rows"] = string(rows)
	}
	applyTableSignals(md, r.Signals)
	e.fail(applyCommonAttrs(md, r.Attrs))
	content := ""
	if !r.Signals.Layout {
		content = renderTableText(r)
	}
	e.nodes = append(e.nodes, &knowledgev1.Node{
		Id:       id,
		Type:     r.recordKind(),
		Content:  content,
		Source:   "web-collect",
		Metadata: md,
	})
	e.addContains(parentID, id, idx)
}

// renderTableText flattens a data table's headers and rows into the plain
// text a content composer can read: one line per row, cells separated by
// " | ", headers first when the table has them.
//
// It is a rendering of the SAME cells the headers/rows metadata already
// carries, not a second extraction — the metadata keeps the structure for a
// consumer that wants the grid, and this keeps the words for one that wants
// the text. Empty rows contribute no line, so a table of nothing renders "".
func renderTableText(r tableRecord) string {
	lines := make([]string, 0, len(r.Rows)+1)
	if len(r.Headers) > 0 {
		lines = append(lines, strings.Join(r.Headers, " | "))
	}
	for _, row := range r.Rows {
		if len(row) == 0 {
			continue
		}
		lines = append(lines, strings.Join(row, " | "))
	}
	return strings.Join(lines, "\n")
}

// applyTableSignals stamps the table classifier's verdict and the four
// measurements that produced it.
//
// table_layout is written for BOTH verdicts rather than only for the layout
// one. An absent key would be ambiguous between "this table was judged data"
// and "this graph was collected before tables carried a verdict", and the
// second is a real state a consumer meets on any older graph.
//
// table_role is omitted when the table declares no role, because an empty
// string is a reading of an attribute that is not there rather than a role of
// "". The other three are always present: they are measurements of the table
// itself, and every table has a row count.
func applyTableSignals(md map[string]string, sig tableSignals) {
	md["table_layout"] = strconv.FormatBool(sig.Layout)
	if sig.Role != "" {
		md["table_role"] = sig.Role
	}
	md["table_header_signal"] = strconv.FormatBool(sig.HeaderSignal)
	md["table_row_count"] = strconv.Itoa(sig.RowCount)
	md["table_uniform"] = strconv.FormatBool(sig.Uniform)
	md["table_cell_has_block"] = strconv.FormatBool(sig.CellHasBlock)
}

// applyListSignals writes a list's navigation verdict and the measurements
// that produced it, by the same rules applyTableSignals follows.
//
// list_nav is written for BOTH verdicts rather than only for the navigation
// one. An absent key would be ambiguous between "this list was judged content"
// and "this graph was collected before lists carried a verdict", and the second
// is a real state a consumer meets on any older graph.
//
// list_role and list_ancestry are omitted when the reading is empty, because no
// role attribute and no sectioning ancestor are ABSENCES rather than values of
// "". The two counts are always present: they are measurements of the list
// itself, and every list has an item count.
func applyListSignals(md map[string]string, sig listSignals) {
	md["list_nav"] = strconv.FormatBool(sig.Nav)
	if sig.Role != "" {
		md["list_role"] = sig.Role
	}
	if sig.Ancestry != "" {
		md["list_ancestry"] = sig.Ancestry
	}
	md["list_item_count"] = strconv.Itoa(sig.ItemCount)
	md["list_link_only_items"] = strconv.Itoa(sig.LinkOnlyItems)
}

// emitLink emits a "link" node under parentID. The link is also
// registered in e.page.InternalLinks / e.page.ExternalCites already,
// so emitLinks() handles page-level reference edges separately. This
// per-section link node captures the in-section anchor.
//
// Two addresses meet on this node and must not be conflated. md["uri"] is
// the link's OWN position in THIS document — the enclosing section's uri,
// passed in. md["url"] and md["anchor"] describe the link's TARGET, which
// lives in some other document; r.Anchor is the fragment of that target URL,
// never an address in this page.
func (e *emitter) emitLink(parentID, path string, r linkRecord, idx int, uri string) {
	myPath := extendPath(path, "link", idx)
	id := stableID(e.page.URL, "link", myPath, idx)
	md := map[string]string{
		"position": strconv.Itoa(idx),
		"url":      r.URL,
		"rel":      r.Rel,
		"uri":      uri,
	}
	if r.Anchor != "" {
		md["anchor"] = r.Anchor
	}
	if r.NoFollow {
		md["nofollow"] = "true"
	}
	e.fail(applyCommonAttrs(md, r.Attrs))
	e.nodes = append(e.nodes, &knowledgev1.Node{
		Id:         id,
		Type:       r.recordKind(),
		SymbolName: r.Text,
		Source:     "web-collect",
		Metadata:   md,
	})
	e.addContains(parentID, id, idx)
}

// emitImage emits an "image" node. uri is the enclosing section's address;
// md["url"] remains the image's own source URL.
func (e *emitter) emitImage(parentID, path string, r imageRecord, idx int, uri string) {
	myPath := extendPath(path, "image", idx)
	id := stableID(e.page.URL, "image", myPath, idx)
	md := map[string]string{
		"position": strconv.Itoa(idx),
		"url":      r.URL,
		"uri":      uri,
	}
	if r.Alt != "" {
		md["alt"] = r.Alt
	}
	if r.Caption != "" {
		md["caption"] = r.Caption
	}
	e.fail(applyCommonAttrs(md, r.Attrs))
	e.nodes = append(e.nodes, &knowledgev1.Node{
		Id:       id,
		Type:     r.recordKind(),
		Source:   "web-collect",
		Metadata: md,
	})
	e.addContains(parentID, id, idx)
}

// emitQuote emits a "blockquote" node. uri is the enclosing section's
// address, stamped on the node.
func (e *emitter) emitQuote(parentID, path string, r quoteRecord, idx int, uri string) {
	myPath := extendPath(path, "blockquote", idx)
	id := stableID(e.page.URL, "blockquote", myPath, idx)
	md := map[string]string{"position": strconv.Itoa(idx), "uri": uri}
	if r.CiteURL != "" {
		md["cite_url"] = r.CiteURL
	}
	e.fail(applyCommonAttrs(md, r.Attrs))
	e.fail(applyInlineEmphasis(md, r.InlineEmphasis))
	e.nodes = append(e.nodes, &knowledgev1.Node{
		Id:       id,
		Type:     r.recordKind(),
		Content:  r.Text,
		Source:   "web-collect",
		Metadata: md,
	})
	e.addContains(parentID, id, idx)
}

// runeLen returns the RUNE count of s as a decimal string, for the
// text_length metadata key.
//
// RUNES, NOT BYTES, and the distinction is load-bearing rather than
// stylistic: text_length exists so a consumer can see the measurement the
// heading heuristic compared, and that heuristic measures runes — the marker
// candidate's textLen is len([]rune(text)) in parse_dom_headings.go. A byte
// count would be the same number only for pure ASCII, so on any page with
// accented or non-Latin prose it would silently not be the figure the
// classifier weighed.
//
// It is stamped on the PROSE-BEARING kinds only. code_block, table and
// blockquote carry their text elsewhere and take no part in that length
// comparison, so a length on them would imply a role in a calibration they
// never enter.
func runeLen(s string) string {
	return strconv.Itoa(len([]rune(s)))
}
