// SPDX-License-Identifier: Apache-2.0

package web

import (
	"reflect"
	"strings"
	"testing"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// parseEmphasisFragment parses a fragment as the body of a <p> and
// returns the <p> node. Using html.ParseFragment keeps the tests honest
// about how the net/html parser tokenizes inline runs.
func parseEmphasisFragment(t *testing.T, inner string) *html.Node {
	t.Helper()
	body := &html.Node{Type: html.ElementNode, Data: "body", DataAtom: atom.Body}
	nodes, err := html.ParseFragment(strings.NewReader("<p>"+inner+"</p>"), body)
	if err != nil {
		t.Fatalf("parse fragment: %v", err)
	}
	for _, n := range nodes {
		if n.Type == html.ElementNode && n.Data == "p" {
			return n
		}
		// Some fragments wrap <p> under implicit elements — walk descendants.
		if found := findParagraph(n); found != nil {
			return found
		}
	}
	t.Fatalf("no <p> in parsed fragment: %q", inner)
	return nil
}

func findParagraph(n *html.Node) *html.Node {
	if n == nil {
		return nil
	}
	if n.Type == html.ElementNode && n.Data == "p" {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if found := findParagraph(c); found != nil {
			return found
		}
	}
	return nil
}

func TestEmitProseTextWithEmphasis_MixedRuns(t *testing.T) {
	p := parseEmphasisFragment(t, "a <strong>bold</strong> c <em>it</em> d")
	text, emphs := emitProseTextWithEmphasis(p)

	wantText := "a bold c it d"
	if text != wantText {
		t.Fatalf("text = %q, want %q", text, wantText)
	}
	if len(emphs) != 2 {
		t.Fatalf("want 2 emphasis entries, got %d: %+v", len(emphs), emphs)
	}
	// Entry 0: strong "bold" at offset 2.
	if emphs[0].Tag != "strong" || emphs[0].Text != "bold" || emphs[0].Position != 2 {
		t.Errorf("entry 0 = %+v, want {strong bold 2}", emphs[0])
	}
	// Entry 1: em "it" at offset 9 (after "a bold c ").
	if emphs[1].Tag != "em" || emphs[1].Text != "it" || emphs[1].Position != 9 {
		t.Errorf("entry 1 = %+v, want {em it 9}", emphs[1])
	}
	// Sanity: substring at recorded positions matches Text.
	for _, e := range emphs {
		end := e.Position + len(e.Text)
		if end > len(text) || text[e.Position:end] != e.Text {
			t.Errorf("position %d/%q does not match text[%d:%d]=%q",
				e.Position, e.Text, e.Position, end, sliceOrEmpty(text, e.Position, end))
		}
	}
}

func TestEmitProseTextWithEmphasis_Nested(t *testing.T) {
	// Outer <strong> wins; inner <em> contributes text only.
	p := parseEmphasisFragment(t, "hi <strong>bold <em>and</em> big</strong> done")
	text, emphs := emitProseTextWithEmphasis(p)

	wantText := "hi bold and big done"
	if text != wantText {
		t.Fatalf("text = %q, want %q", text, wantText)
	}
	if len(emphs) != 1 {
		t.Fatalf("want 1 emphasis entry (outer strong wins), got %d: %+v", len(emphs), emphs)
	}
	got := emphs[0]
	wantEntry := inlineEmphasis{Tag: "strong", Text: "bold and big", Position: 3}
	if got != wantEntry {
		t.Errorf("entry = %+v, want %+v", got, wantEntry)
	}
}

func TestEmitProseTextWithEmphasis_AllTags(t *testing.T) {
	p := parseEmphasisFragment(t,
		"<strong>S</strong> <em>E</em> <code>C</code> <b>B</b> <i>I</i> <kbd>K</kbd>")
	text, emphs := emitProseTextWithEmphasis(p)

	wantText := "S E C B I K"
	if text != wantText {
		t.Fatalf("text = %q, want %q", text, wantText)
	}
	wantTags := []string{"strong", "em", "code", "b", "i", "kbd"}
	if len(emphs) != len(wantTags) {
		t.Fatalf("want %d entries, got %d: %+v", len(wantTags), len(emphs), emphs)
	}
	gotTags := make([]string, len(emphs))
	for i, e := range emphs {
		gotTags[i] = e.Tag
	}
	if !reflect.DeepEqual(gotTags, wantTags) {
		t.Errorf("tags = %v, want %v", gotTags, wantTags)
	}
	// Positions should be 0, 2, 4, 6, 8, 10 (each single char followed by space).
	wantPositions := []int{0, 2, 4, 6, 8, 10}
	for i, e := range emphs {
		if e.Position != wantPositions[i] {
			t.Errorf("entry %d position = %d, want %d (entry=%+v)",
				i, e.Position, wantPositions[i], e)
		}
	}
}

func TestEmitProseTextWithEmphasis_NoEmphasis(t *testing.T) {
	p := parseEmphasisFragment(t, "hello world")
	text, emphs := emitProseTextWithEmphasis(p)
	if text != "hello world" {
		t.Errorf("text = %q, want %q", text, "hello world")
	}
	if len(emphs) != 0 {
		t.Errorf("want no emphasis entries, got %+v", emphs)
	}
}

func TestEmitProseTextWithEmphasis_IgnoresNonEmphasis(t *testing.T) {
	p := parseEmphasisFragment(t, `a <span>b</span> <a href="/x">c</a>`)
	text, emphs := emitProseTextWithEmphasis(p)
	if text != "a b c" {
		t.Errorf("text = %q, want %q", text, "a b c")
	}
	if len(emphs) != 0 {
		t.Errorf("want no emphasis entries (span/a not emphasis tags), got %+v", emphs)
	}
}

func TestEmitProseTextWithEmphasis_WhitespaceInsideEmphasis(t *testing.T) {
	// Emphasis inner whitespace collapses; outer whitespace flushes to one
	// space; positions refer to the collapsed text.
	p := parseEmphasisFragment(t, "  x  <em>  inner   run  </em>  y  ")
	text, emphs := emitProseTextWithEmphasis(p)
	if text != "x inner run y" {
		t.Errorf("text = %q, want %q", text, "x inner run y")
	}
	if len(emphs) != 1 {
		t.Fatalf("want 1 entry, got %d: %+v", len(emphs), emphs)
	}
	got := emphs[0]
	wantEntry := inlineEmphasis{Tag: "em", Text: "inner run", Position: 2}
	if got != wantEntry {
		t.Errorf("entry = %+v, want %+v", got, wantEntry)
	}
	// Sanity check positions point at the right substring in text.
	end := got.Position + len(got.Text)
	if got.Position > len(text) || end > len(text) || text[got.Position:end] != got.Text {
		t.Errorf("position slice mismatch: text[%d:%d]=%q want %q",
			got.Position, end, sliceOrEmpty(text, got.Position, end), got.Text)
	}
}

func sliceOrEmpty(s string, lo, hi int) string {
	if lo < 0 || hi < 0 || lo > len(s) || hi > len(s) || lo > hi {
		return ""
	}
	return s[lo:hi]
}
