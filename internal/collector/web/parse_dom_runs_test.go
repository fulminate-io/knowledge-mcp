// SPDX-License-Identifier: Apache-2.0

package web

import (
	"strings"
	"testing"
)

// parseInline runs a small inline document through parsePage.
func parseInline(t *testing.T, title, body string) *pageRecord {
	t.Helper()
	doc := "<html><head><title>" + title + "</title></head><body>" + body + "</body></html>"
	rec, err := parsePage(fakeFetched("https://example.test/page", doc), fakeCleaned(title, doc))
	if err != nil {
		t.Fatalf("parsePage: %v", err)
	}
	return rec
}

// TestParsePage_InlineRunBehaviour pins the run model's emission contract.
//
// The link legs are the ones that matter most: handleAnchor is the ONLY
// thing in this package that appends a linkRecord, so any branch reaching an
// anchor without calling it silently drops that anchor's node — and the
// anchor-transparency branch is not covered by any other criterion here.
func TestParsePage_InlineRunBehaviour(t *testing.T) {
	t.Parallel()

	t.Run("link-only run emits links and no paragraph", func(t *testing.T) {
		t.Parallel()
		got := collectRecords(parseInline(t, "Links", `<div><a href="/one">One</a> <a href="/two">Two</a></div>`))
		if len(got.links) != 2 {
			t.Errorf("got %d link records, want 2 (one per anchor)", len(got.links))
		}
		if len(got.paragraphs) != 0 {
			t.Errorf("got %d paragraphs, want 0 — a run of nothing but links is a navigation strip: %v", len(got.paragraphs), paragraphTexts(got.paragraphs))
		}
	})

	t.Run("mixed run emits one paragraph and one link per anchor", func(t *testing.T) {
		t.Parallel()
		got := collectRecords(parseInline(t, "Mixed", `<div>Some prose with <a href="/three">a link</a> inside it.</div>`))
		if len(got.paragraphs) != 1 {
			t.Fatalf("got %d paragraphs, want 1: %v", len(got.paragraphs), paragraphTexts(got.paragraphs))
		}
		if want := "Some prose with a link inside it."; got.paragraphs[0].Text != want {
			t.Errorf("paragraph text = %q, want %q", got.paragraphs[0].Text, want)
		}
		if len(got.links) != 1 {
			t.Errorf("got %d link records, want 1 — an anchor absorbed into prose still emits its node", len(got.links))
		}
	})

	t.Run("text and p alternate in document order", func(t *testing.T) {
		t.Parallel()
		got := collectRecords(parseInline(t, "Order", `<div>leading text<p>a real paragraph</p>trailing text</div>`))
		want := []string{"leading text", "a real paragraph", "trailing text"}
		gotTexts := paragraphTexts(got.paragraphs)
		if !slicesEqual(gotTexts, want) {
			t.Errorf("paragraph order = %v, want %v", gotTexts, want)
		}
	})

	// THE ANCHOR-TRANSPARENCY LEG. <a>'s content model is transparent, so an
	// anchor may wrap flow content. The section and paragraph legs pass under
	// BOTH the correct spelling (handleAnchor + walkChildren) and the
	// defective one (makeLink + classifyLink, neither of which appends), so
	// the LINK clause is the only automated discriminator between them.
	// Measured on live pages, the defective spelling costs 42 link records on
	// one CWE page alone and drops Hohpe below its pre-fix baseline.
	t.Run("anchor wrapping block content yields section, paragraph and link", func(t *testing.T) {
		t.Parallel()
		got := collectRecords(parseInline(t, "Masthead",
			`<a href="/home"><h1>Masthead Heading</h1><p>Masthead prose.</p></a>`))

		headed := 0
		for _, s := range got.sections {
			if strings.TrimSpace(s.Heading) != "" {
				headed++
			}
		}
		if headed != 1 {
			t.Errorf("got %d headed sections, want 1 — the wrapped <h1> must push a section, not be swallowed", headed)
		}
		if len(got.paragraphs) != 1 {
			t.Errorf("got %d paragraphs, want 1 — the wrapped <p> must survive: %v", len(got.paragraphs), paragraphTexts(got.paragraphs))
		}
		if len(got.links) != 1 {
			t.Errorf("got %d link records, want 1 — the anchor's own node must still be emitted; makeLink and classifyLink do not append, only handleAnchor does", len(got.links))
		}
	})
}

func paragraphTexts(paras []paragraphRecord) []string {
	out := make([]string, 0, len(paras))
	for _, p := range paras {
		out = append(out, p.Text)
	}
	return out
}

// TestParsePage_HeadSubtreeNotEmitted pins the <head> skip. The skip is only
// ever REACHED because the phrasing-keyed partition makes <html> and <head>
// block-level, so walkChildren hands <head> to walk rather than accumulating
// its <title> text into a run.
func TestParsePage_HeadSubtreeNotEmitted(t *testing.T) {
	t.Parallel()

	const titleToken = "UniqueTitleTokenNotInBody"
	const bodyToken = "body prose that must survive"
	rec := parseInline(t, titleToken, `<p>`+bodyToken+`</p>`)
	got := collectRecords(rec)

	for i, p := range got.paragraphs {
		if strings.Contains(p.Text, titleToken) {
			t.Errorf("paragraph %d carries the page title text %q: %q", i, titleToken, p.Text)
		}
	}
	if rec.Title != titleToken {
		t.Errorf("rec.Title = %q, want %q — the title must still reach the record through the cleaned article", rec.Title, titleToken)
	}
	// Known-positive: the run model must still emit ordinary body prose, so a
	// walker that emitted nothing at all cannot pass this test.
	if !anyParagraphContains(got.paragraphs, bodyToken) {
		t.Errorf("body prose %q was not emitted; got %v", bodyToken, paragraphTexts(got.paragraphs))
	}
}

// TestParsePage_CustomElementContainer detects the DataAtom==0 hazard.
// golang.org/x/net/html assigns atom 0 to every element outside its table,
// so a partition keyed on KNOWN block atoms reads <x-widget> — and every
// Astro/Lit/Stencil/awsui wrapper — as inline and swallows its subtree.
func TestParsePage_CustomElementContainer(t *testing.T) {
	t.Parallel()

	got := collectRecords(parseInline(t, "Custom", `<x-widget><p>first child paragraph</p><p>second child paragraph</p></x-widget>`))
	if len(got.paragraphs) != 2 {
		t.Errorf("got %d paragraphs, want 2 — a custom-element container must not swallow its children: %v",
			len(got.paragraphs), paragraphTexts(got.paragraphs))
	}
}

// TestParsePage_BlockNestedInPhrasing pins the promotion clause: a phrasing
// element that WRAPS block content breaks the run rather than swallowing it.
// Measured loss without it on the live go101 corpus: 2 lists and 7 list
// items.
func TestParsePage_BlockNestedInPhrasing(t *testing.T) {
	t.Parallel()

	got := collectRecords(parseInline(t, "Nested",
		`<div>lead in <small><div><ul><li>first item</li><li>second item</li></ul></div></small></div>`))

	if len(got.lists) != 1 {
		t.Fatalf("got %d listRecords, want 1 — the <ul> inside <small><div> was swallowed into an inline run", len(got.lists))
	}
	if len(got.lists[0].Items) != 2 {
		t.Errorf("list kept %d items, want 2", len(got.lists[0].Items))
	}
	if len(got.listItems) != 2 {
		t.Errorf("got %d list items overall, want 2", len(got.listItems))
	}

	// The same shape as it actually appears in a checked-in fixture, so the
	// assertion is tied to a real artifact rather than only to an inline
	// string this test wrote for itself. divprose_blocks.html reproduces the
	// live go101 markup, where the <ul> sits inside <small><div>.
	fixture := collectRecords(parseFixture(t, "divprose_blocks.html"))
	if len(fixture.lists) != 1 || len(fixture.listItems) != 2 {
		t.Errorf("divprose_blocks.html yielded %d lists / %d list items, want 1 / 2 — the <small><div><ul> subtree was swallowed",
			len(fixture.lists), len(fixture.listItems))
	}
}

// codeExampleRecords returns the section holding the fixture's code example
// plus the ordered index of the label record and the code record within it.
func codeExampleRecords(t *testing.T) (*sectionRecord, int, int) {
	t.Helper()
	rec := parseFixture(t, "cwe_table_layout.html")

	var sections []*sectionRecord
	var walkSection func(*sectionRecord)
	walkSection = func(s *sectionRecord) {
		if s == nil {
			return
		}
		sections = append(sections, s)
		for _, child := range s.Children {
			if ns, ok := child.(nestedSectionRecord); ok {
				walkSection(ns.Section)
			}
		}
	}
	for _, s := range rec.TopSections {
		walkSection(s)
	}

	for _, s := range sections {
		label, code := -1, -1
		for i, child := range s.Children {
			p, ok := child.(paragraphRecord)
			if !ok {
				continue
			}
			if p.Text == "(bad code)" {
				label = i
			}
			if strings.Contains(p.Text, "shell_exec($listing);") {
				code = i
			}
		}
		if label >= 0 && code >= 0 {
			return s, label, code
		}
	}
	t.Fatalf("no section holds both the code label record and the code record")
	return nil, -1, -1
}

// TestParsePage_CodeExample_TextAndLines is leg 1 of 3: the code text
// survives with the author's line structure intact, proving the <br>
// separator and collapseProseLines end to end.
func TestParsePage_CodeExample_TextAndLines(t *testing.T) {
	t.Parallel()
	sec, _, code := codeExampleRecords(t)

	text := sec.Children[code].(paragraphRecord).Text
	wantLines := []string{
		`$account = $_GET["account"];`,
		`$listing = 'ls -l /srv/accounts/' . $account;`,
		`shell_exec($listing);`,
	}
	gotLines := strings.Split(text, "\n")
	if !slicesEqual(gotLines, wantLines) {
		t.Errorf("code record lines\n got %q\nwant %q", gotLines, wantLines)
	}
}

// TestParsePage_CodeExample_LabelRecord is leg 2 of 3: the label survives as
// its own record and PRECEDES the code record in document order under the
// same section. This adjacency is the discriminator a recipe keys on.
func TestParsePage_CodeExample_LabelRecord(t *testing.T) {
	t.Parallel()
	sec, label, code := codeExampleRecords(t)

	if got := sec.Children[label].(paragraphRecord).Text; got != "(bad code)" {
		t.Errorf("label record text = %q, want exactly %q", got, "(bad code)")
	}
	if label >= code {
		t.Errorf("label record is at index %d and the code record at %d — the label must PRECEDE the code in document order", label, code)
	}
}

// TestParsePage_CodeExample_AttrsFromNearestClassedAncestor is leg 3 of 3: a
// run has no element of its own and its immediate parent is unclassed here,
// so attrs must come from the nearest CLASSED ancestor. Taking them from the
// bare parent yields empty class and id on exactly the records a recipe
// needs.
func TestParsePage_CodeExample_AttrsFromNearestClassedAncestor(t *testing.T) {
	t.Parallel()

	// Anchor the expectation in the fixture: if these classes are ever
	// renamed there, this test must fail loudly rather than pass vacuously
	// against stale literals.
	raw := string(loadFixture(t, "cwe_table_layout.html"))
	for _, cls := range []string{`class="top"`, `class="CodeHead"`} {
		if !strings.Contains(raw, cls) {
			t.Fatalf("fixture no longer carries %s — the expectation below is stale", cls)
		}
	}
	// The immediate parents of both runs are unclassed, which is the whole
	// reason nearestAttrSource exists.
	if strings.Contains(raw, `class="top"><div class=`) {
		t.Fatalf("fixture's div.top child is now classed — the nearest-classed-ancestor walk is no longer exercised")
	}

	sec, label, code := codeExampleRecords(t)

	if got := sec.Children[code].(paragraphRecord).Attrs.Class; got != "top" {
		t.Errorf("code record Attrs.Class = %q, want %q", got, "top")
	}
	if got := sec.Children[label].(paragraphRecord).Attrs.Class; got != "CodeHead" {
		t.Errorf("label record Attrs.Class = %q, want %q", got, "CodeHead")
	}
}
