// SPDX-License-Identifier: Apache-2.0

package web

import (
	"strings"
	"testing"

	"golang.org/x/net/html"
)

// TestPruneHiddenNodes_DropsHiddenAttribute confirms HTML5 boolean
// `hidden` removes the element + subtree. Specifically the Microsoft
// Learn shape: <div id="unsupported-browser" hidden>...browser
// boilerplate...</div>. Without the prune, "This browser is no longer
// supported" leaks into Description on every Azure pattern page.
func TestPruneHiddenNodes_DropsHiddenAttribute(t *testing.T) {
	doc := mustParse(t, `<html><body>
<div id="unsupported-browser" hidden>
  <p>This browser is no longer supported.</p>
</div>
<h1>Saga Design Pattern</h1>
<p>Real article body.</p>
</body></html>`)

	pruneHiddenNodes(doc)
	out := renderHTML(doc)
	if strings.Contains(out, "no longer supported") {
		t.Errorf("hidden subtree leaked through prune:\n%s", out)
	}
	if !strings.Contains(out, "Real article body") {
		t.Errorf("visible content was incorrectly removed:\n%s", out)
	}
}

// TestPruneHiddenNodes_DropsAriaHiddenTrue confirms aria-hidden="true"
// is treated as not-rendered. Wikipedia uses this on decorative spans
// (icon labels, dropdown chevrons, language-list collapse toggles).
func TestPruneHiddenNodes_DropsAriaHiddenTrue(t *testing.T) {
	doc := mustParse(t, `<html><body>
<span aria-hidden="true">decorative chrome</span>
<p aria-hidden="false">live caption</p>
<p>visible prose</p>
</body></html>`)

	pruneHiddenNodes(doc)
	out := renderHTML(doc)
	if strings.Contains(out, "decorative chrome") {
		t.Errorf("aria-hidden=true element leaked:\n%s", out)
	}
	if !strings.Contains(out, "live caption") {
		t.Errorf("aria-hidden=false was wrongly pruned:\n%s", out)
	}
	if !strings.Contains(out, "visible prose") {
		t.Errorf("plain visible content was wrongly pruned:\n%s", out)
	}
}

// TestPruneHiddenNodes_DropsInlineDisplayNone confirms inline
// style="display:none" is treated as hidden. Whitespace-tolerant on the
// colon and inside the property value.
func TestPruneHiddenNodes_DropsInlineDisplayNone(t *testing.T) {
	cases := []struct {
		name string
		html string
		want string
	}{
		{
			name: "display:none compact",
			html: `<div style="display:none">chrome A</div><p>kept A</p>`,
			want: "kept A",
		},
		{
			name: "display: none with space",
			html: `<div style="display: none">chrome B</div><p>kept B</p>`,
			want: "kept B",
		},
		{
			name: "DISPLAY: NONE uppercase",
			html: `<div style="DISPLAY: NONE">chrome C</div><p>kept C</p>`,
			want: "kept C",
		},
		{
			name: "visibility:hidden",
			html: `<div style="visibility: hidden">chrome D</div><p>kept D</p>`,
			want: "kept D",
		},
		{
			name: "mixed declarations with display:none",
			html: `<div style="color: red; display: none; padding: 8px">chrome E</div><p>kept E</p>`,
			want: "kept E",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			doc := mustParse(t, "<html><body>"+c.html+"</body></html>")
			pruneHiddenNodes(doc)
			out := renderHTML(doc)
			if !strings.Contains(out, c.want) {
				t.Errorf("expected %q kept, got:\n%s", c.want, out)
			}
			if strings.Contains(out, "chrome") {
				t.Errorf("hidden element leaked:\n%s", out)
			}
		})
	}
}

// TestPruneHiddenNodes_KeepsVisibleStyles confirms inline styles that
// don't hide the element (color, padding, font-size) are no-op for
// the prune. Microsoft Learn's actual content paragraphs carry style
// attributes for typography, and dropping them would lose the article.
func TestPruneHiddenNodes_KeepsVisibleStyles(t *testing.T) {
	doc := mustParse(t, `<html><body>
<p style="font-size: 24px; color: red">visible heading</p>
<p style="display: block">visible block</p>
<p style="visibility: visible">visible explicit</p>
</body></html>`)

	pruneHiddenNodes(doc)
	out := renderHTML(doc)
	for _, want := range []string{"visible heading", "visible block", "visible explicit"} {
		if !strings.Contains(out, want) {
			t.Errorf("visible content %q wrongly removed:\n%s", want, out)
		}
	}
}

// TestPruneHiddenNodes_NestedHiddenSubtree confirms when a parent is
// hidden, the entire subtree goes regardless of child visibility — same
// behavior as a real browser's render filter.
func TestPruneHiddenNodes_NestedHiddenSubtree(t *testing.T) {
	doc := mustParse(t, `<html><body>
<div hidden>
  <div>
    <p>nested chrome that should not survive</p>
    <ul><li>also gone</li></ul>
  </div>
</div>
<p>top-level visible</p>
</body></html>`)

	pruneHiddenNodes(doc)
	out := renderHTML(doc)
	if strings.Contains(out, "nested chrome") || strings.Contains(out, "also gone") {
		t.Errorf("descendants of hidden parent leaked:\n%s", out)
	}
	if !strings.Contains(out, "top-level visible") {
		t.Errorf("sibling of hidden parent was wrongly removed:\n%s", out)
	}
}

// TestPruneHiddenNodes_NilSafe confirms nil root is a no-op rather than
// a crash — defensive against the html.Parse error path the caller
// already gates on but might not in the future.
func TestPruneHiddenNodes_NilSafe(t *testing.T) {
	pruneHiddenNodes(nil)
}

// --- helpers ----------------------------------------------------------

func mustParse(t *testing.T, s string) *html.Node {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(s))
	if err != nil {
		t.Fatalf("html.Parse: %v", err)
	}
	return doc
}

func renderHTML(n *html.Node) string {
	var b strings.Builder
	if err := html.Render(&b, n); err != nil {
		return "<render error: " + err.Error() + ">"
	}
	return b.String()
}
