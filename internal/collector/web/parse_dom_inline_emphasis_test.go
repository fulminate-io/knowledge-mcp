// SPDX-License-Identifier: Apache-2.0

package web

import "testing"

// TestParsePage_ParagraphInlineEmphasis covers end-to-end parsing of an
// inline <code> span on a paragraph: the flattened Text keeps its v0
// backtick wrapping AND paragraphRecord.InlineEmphasis gains a {code, ...}
// entry with the collapsed-offset position.
func TestParsePage_ParagraphInlineEmphasis(t *testing.T) {
	html := `<html><body>
<h1>T</h1>
<p>set <code>FOO</code> first</p>
</body></html>`
	p := fakeFetched("https://example.com/p", html)
	rec, err := parsePage(p, fakeCleaned("", html))
	if err != nil {
		t.Fatalf("parsePage: %v", err)
	}
	para := findParagraphRecord(rec)
	if para == nil {
		t.Fatalf("no paragraphRecord in parsed page")
	}
	// Text preserves backtick-wrapped <code> (v0 behavior, unchanged).
	if para.Text != "set `FOO` first" {
		t.Fatalf("Text = %q, want %q", para.Text, "set `FOO` first")
	}
	if len(para.InlineEmphasis) != 1 {
		t.Fatalf("want 1 emphasis entry, got %+v", para.InlineEmphasis)
	}
	got := para.InlineEmphasis[0]
	// Positions reference the UNBACKTICKED collapsed text "set FOO first",
	// so FOO starts at offset 4.
	want := inlineEmphasis{Tag: "code", Text: "FOO", Position: 4}
	if got != want {
		t.Errorf("entry = %+v, want %+v", got, want)
	}
}

// TestParsePage_ListItemInlineEmphasis covers <li> with an inline <em>.
func TestParsePage_ListItemInlineEmphasis(t *testing.T) {
	html := `<html><body>
<h1>T</h1>
<ul><li>pick <em>this</em> one</li></ul>
</body></html>`
	p := fakeFetched("https://example.com/p", html)
	rec, err := parsePage(p, fakeCleaned("", html))
	if err != nil {
		t.Fatalf("parsePage: %v", err)
	}
	item := findFirstListItem(rec)
	if item == nil {
		t.Fatalf("no listItemRecord in parsed page")
	}
	if len(item.InlineEmphasis) != 1 {
		t.Fatalf("want 1 emphasis entry, got %+v", item.InlineEmphasis)
	}
	got := item.InlineEmphasis[0]
	want := inlineEmphasis{Tag: "em", Text: "this", Position: 5}
	if got != want {
		t.Errorf("entry = %+v, want %+v", got, want)
	}
}

// TestParsePage_BlockquoteInlineEmphasis covers <blockquote> with inline
// <strong>. Blockquotes gained emphasis support in Phase 6 (OQ1 answer).
func TestParsePage_BlockquoteInlineEmphasis(t *testing.T) {
	html := `<html><body>
<h1>T</h1>
<blockquote>always <strong>done</strong> well</blockquote>
</body></html>`
	p := fakeFetched("https://example.com/p", html)
	rec, err := parsePage(p, fakeCleaned("", html))
	if err != nil {
		t.Fatalf("parsePage: %v", err)
	}
	q := findQuoteRecord(rec)
	if q == nil {
		t.Fatalf("no quoteRecord in parsed page")
	}
	if len(q.InlineEmphasis) != 1 {
		t.Fatalf("want 1 emphasis entry, got %+v", q.InlineEmphasis)
	}
	got := q.InlineEmphasis[0]
	want := inlineEmphasis{Tag: "strong", Text: "done", Position: 7}
	if got != want {
		t.Errorf("entry = %+v, want %+v", got, want)
	}
}

// --- record-finding helpers (also used by emit_nodes tests) ---

func findParagraphRecord(rec *pageRecord) *paragraphRecord {
	for _, s := range rec.TopSections {
		if p := findParagraphIn(s); p != nil {
			return p
		}
	}
	return nil
}

func findParagraphIn(s *sectionRecord) *paragraphRecord {
	for _, c := range s.Children {
		switch v := c.(type) {
		case paragraphRecord:
			return &v
		case nestedSectionRecord:
			if p := findParagraphIn(v.Section); p != nil {
				return p
			}
		}
	}
	return nil
}

func findFirstListItem(rec *pageRecord) *listItemRecord {
	for _, s := range rec.TopSections {
		if li := findListItemIn(s); li != nil {
			return li
		}
	}
	return nil
}

func findListItemIn(s *sectionRecord) *listItemRecord {
	for _, c := range s.Children {
		switch v := c.(type) {
		case listRecord:
			if len(v.Items) > 0 {
				item := v.Items[0]
				return &item
			}
		case nestedSectionRecord:
			if li := findListItemIn(v.Section); li != nil {
				return li
			}
		}
	}
	return nil
}

func findQuoteRecord(rec *pageRecord) *quoteRecord {
	for _, s := range rec.TopSections {
		if q := findQuoteIn(s); q != nil {
			return q
		}
	}
	return nil
}

func findQuoteIn(s *sectionRecord) *quoteRecord {
	for _, c := range s.Children {
		switch v := c.(type) {
		case quoteRecord:
			return &v
		case nestedSectionRecord:
			if q := findQuoteIn(v.Section); q != nil {
				return q
			}
		}
	}
	return nil
}
