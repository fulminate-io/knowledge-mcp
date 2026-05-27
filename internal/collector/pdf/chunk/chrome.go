// chrome.go — running header / footer detection via cross-page text
// repetition.
//
// PDFs without a /StructTreeRoot don't tell us which blocks are running
// chrome (page headers, page footers, recurring chapter titles). The
// upstream Page.HeadersFooters() returns ErrNotImplemented, so v1's
// chunker has been letting that chrome flow through to classification —
// the consequence is that a chapter's running header ("Chapter 3:
// Storage and Retrieval | 102") gets re-classified as a heading on
// every page it appears, polluting the section tree with hundreds of
// duplicate "section" nodes.
//
// stripRepeatedChrome detects this case by fingerprinting block text
// across pages: any block whose digit-normalized text recurs on >= 3
// distinct pages is treated as chrome and removed. The threshold is
// deliberately lenient — real running headers always span multiple
// pages, while real body content (even repeated callouts) rarely
// crosses the >=3-page boundary verbatim. Title-page headings escape
// because they appear on a single page even when their text recurs
// elsewhere via a running header (the running header carries an
// appended "| <pagenum>" or leading "<pagenum> | " that distinguishes
// it from the title-page heading after digit-normalization).
//
// Limitations:
//
//   - Pure-text running headers without page numbers (rare) collide with
//     the chapter-title-page heading. Both get flagged as chrome — the
//     legitimate title-page heading is lost.
//   - Repetitive callout boxes ("Note:", "Warning:") with identical
//     boilerplate across >=3 pages would be flagged. In practice these
//     have distinct body text below the marker, which keeps the block
//     fingerprint distinct.
//
// The detector runs only when opts.SkipHeadersFooters is true (the
// default). Callers can disable it by setting the option false; any
// blocks the upstream Document.PageHeadersFooters returns are still
// subtracted independently.

package chunk

import (
	"strings"
	"unicode"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/layout"
)

// chromeMinPages is the threshold for cross-page repetition. A block
// fingerprint observed on >= this many distinct pages is treated as
// running chrome.
const chromeMinPages = 3

// chromeMinPagesShaped is the relaxed threshold applied to blocks
// whose original (pre-normalization) text contains a "<text> | <num>"
// or "<num> | <text>" pattern — the standard O'Reilly running-header
// format. Real body content with that pattern is rare, so two
// occurrences is enough to flag.
const chromeMinPagesShaped = 2

// chromeFingerprintMinLen rejects fingerprints shorter than this many
// runes. Page numbers and short isolated tokens degenerate to empty or
// 1-2-char fingerprints after digit-stripping; we don't want those to
// inflate the per-fingerprint page set.
const chromeFingerprintMinLen = 3

// chromeEntry tracks one block-by-fingerprint occurrence: page index,
// block index within the page, and whether the block matches the
// strict chrome shape ("digits | text" or "text | digits").
type chromeEntry struct {
	page   int
	blk    int
	shaped bool
}

// chromeIndex collects fingerprint occurrences across the document.
type chromeIndex struct {
	pages    map[string]map[int]struct{} // fingerprint → set of pages
	entries  map[string][]chromeEntry    // fingerprint → block occurrences
	shapedFP map[string]bool             // fingerprint had ≥1 shaped match
}

// indexChromeFingerprints walks every block, computes its
// digit-normalized fingerprint, and records page + shape data per
// fingerprint. Building the index up front lets stripRepeatedChrome
// stay flat — the threshold + drop logic reads from this struct.
func indexChromeFingerprints(perPage [][]layout.Block) chromeIndex {
	idx := chromeIndex{
		pages:    make(map[string]map[int]struct{}),
		entries:  make(map[string][]chromeEntry),
		shapedFP: make(map[string]bool),
	}
	for pi, blocks := range perPage {
		for bi, b := range blocks {
			f := chromeFingerprint(b)
			if f == "" {
				continue
			}
			if _, ok := idx.pages[f]; !ok {
				idx.pages[f] = make(map[int]struct{})
			}
			idx.pages[f][pi] = struct{}{}
			isShaped := hasChromeShape(b)
			idx.entries[f] = append(idx.entries[f], chromeEntry{pi, bi, isShaped})
			if isShaped {
				idx.shapedFP[f] = true
			}
		}
	}
	return idx
}

// dropSetFromChromeIndex returns the set of (page,blk) coordinates to
// drop given the indexed fingerprint occurrences. A fingerprint
// qualifies as chrome when its block count crosses the per-shape
// threshold; only entries matching the chrome shape are dropped (real
// headings on title pages share the fingerprint but aren't shaped).
func dropSetFromChromeIndex(idx chromeIndex) map[struct{ page, blk int }]struct{} {
	drop := make(map[struct{ page, blk int }]struct{})
	for f, pages := range idx.pages {
		threshold := chromeMinPages
		if idx.shapedFP[f] {
			threshold = chromeMinPagesShaped
		}
		if len(pages) < threshold {
			continue
		}
		for _, e := range idx.entries[f] {
			if idx.shapedFP[f] && !e.shaped {
				continue
			}
			drop[struct{ page, blk int }{e.page, e.blk}] = struct{}{}
		}
	}
	return drop
}

// stripRepeatedChrome returns a copy of perPage with running-header /
// running-footer blocks removed. Detection: a block is chrome if its
// digit-normalized fingerprint recurs on >= chromeMinPages distinct
// pages.
func stripRepeatedChrome(perPage [][]layout.Block) [][]layout.Block {
	drop := dropSetFromChromeIndex(indexChromeFingerprints(perPage))
	if len(drop) == 0 {
		return perPage
	}
	out := make([][]layout.Block, len(perPage))
	for pi, blocks := range perPage {
		if len(blocks) == 0 {
			out[pi] = blocks
			continue
		}
		kept := make([]layout.Block, 0, len(blocks))
		for bi, b := range blocks {
			if _, dropped := drop[struct{ page, blk int }{pi, bi}]; dropped {
				continue
			}
			kept = append(kept, b)
		}
		out[pi] = kept
	}
	return out
}

// hasChromeShape reports whether b's raw text matches the
// "<text> | <num-or-roman>" or "<num-or-roman> | <text>" running-header
// pattern. Used to relax the cross-page threshold for chrome-shaped
// blocks (real body content rarely has this token).
func hasChromeShape(b layout.Block) bool {
	if len(b.Lines) != 1 {
		return false
	}
	var sb strings.Builder
	for _, r := range b.Lines[0].Runs {
		sb.WriteString(r.Text)
	}
	text := strings.TrimSpace(sb.String())
	before, after, ok := strings.Cut(text, " | ")
	if !ok {
		return false
	}
	left := strings.TrimSpace(before)
	right := strings.TrimSpace(after)
	return isShapedToken(left) || isShapedToken(right)
}

// isShapedToken returns true for tokens consisting only of ASCII
// digits or short Roman-numeral letters (mirrors classify's chrome
// token rule, kept inline so the chunk package doesn't import classify).
func isShapedToken(s string) bool {
	if s == "" || len(s) > 8 {
		return false
	}
	allDigits := true
	allRoman := true
	for _, r := range s {
		if r < '0' || r > '9' {
			allDigits = false
		}
		switch r {
		case 'i', 'v', 'x', 'l', 'c', 'd', 'm', 'I', 'V', 'X', 'L', 'C', 'D', 'M':
		default:
			allRoman = false
		}
		if !allDigits && !allRoman {
			return false
		}
	}
	return allDigits || allRoman
}

// chromeFingerprint computes a stable identifier for a block's text
// content that is robust to per-page page-number variation. Returns
// the empty string for blocks too short or numeric-only to participate
// in the cross-page count.
//
// Normalization:
//
//   - digits → '#' (so "Maintainability | 17" and "Maintainability | 18"
//     share a fingerprint)
//   - uppercase → lowercase
//   - trailing/leading "| <roman-or-#>" stripped (so "Table of Contents
//     | vii" and "Table of Contents | viii" share a fingerprint)
//
// The Roman-numeral strip is only applied when the token sits next to
// a pipe separator at a string edge — it does not alter Roman-looking
// substrings inside body text.
func chromeFingerprint(b layout.Block) string {
	if len(b.Lines) == 0 {
		return ""
	}
	var sb strings.Builder
	for i, l := range b.Lines {
		if i > 0 {
			sb.WriteByte(' ')
		}
		sb.WriteString(joinLineRuns(l))
	}
	raw := sb.String()
	var norm strings.Builder
	norm.Grow(len(raw))
	for _, r := range raw {
		switch {
		case unicode.IsDigit(r):
			norm.WriteByte('#')
		case unicode.IsUpper(r):
			norm.WriteRune(unicode.ToLower(r))
		default:
			norm.WriteRune(r)
		}
	}
	s := stripPipeEdgeToken(collapseWhitespace(norm.String()))
	if utf8RuneCount(s) < chromeFingerprintMinLen {
		return ""
	}
	return s
}

// stripPipeEdgeToken removes a trailing "| <token>" or leading "<token>
// |" where token is purely digits ('#'), Roman-numeral letters, or a
// short alphanumeric run. The pattern matches O'Reilly-style running
// headers ("Section title | <pagenum>" on right pages, "<pagenum> |
// Chapter title" on left pages) so per-page-number variation collapses
// to a single fingerprint.
func stripPipeEdgeToken(s string) string {
	if i := strings.LastIndex(s, "|"); i >= 0 {
		tail := strings.TrimSpace(s[i+1:])
		if isPipeEdgeToken(tail) {
			s = strings.TrimSpace(s[:i])
		}
	}
	if i := strings.Index(s, "|"); i >= 0 {
		head := strings.TrimSpace(s[:i])
		if isPipeEdgeToken(head) {
			s = strings.TrimSpace(s[i+1:])
		}
	}
	return s
}

// isPipeEdgeToken reports whether s is a short token consisting only
// of digit-placeholder ('#') and Roman-numeral letters (i, v, x, l, c,
// d, m). Empty s returns false so we don't strip "| " alone.
func isPipeEdgeToken(s string) bool {
	if s == "" || len(s) > 8 {
		return false
	}
	for _, r := range s {
		switch r {
		case '#', 'i', 'v', 'x', 'l', 'c', 'd', 'm':
			continue
		default:
			return false
		}
	}
	return true
}

// utf8RuneCount counts runes in s without an extra package import.
// Inlined here because chrome.go has no other utf8 dependency.
func utf8RuneCount(s string) int {
	n := 0
	for range s {
		n++
	}
	return n
}
