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
// stampRepeatedChrome detects this case by fingerprinting block text
// across pages, and STAMPS what it finds rather than acting on it: any
// block whose digit-normalized text recurs on two or more distinct
// pages gains page_repeat_count, chrome_repeat_shaped and, where the
// occurrence matches the "<text> | <pagenum>" running-header form,
// chrome_shape. Nothing is removed.
//
// WHY STAMPING RATHER THAN DELETING. The rule this replaced deleted
// every block whose fingerprint recurred on three or more pages, with
// no log and no counter, which meant three separate problems. Content
// that legitimately repeats verbatim was destroyed exactly as
// thoroughly as a running header. A pure-text running header without a
// page number collided with the chapter-title-page heading and took the
// heading with it. And a consumer who disagreed with any of it had
// nothing to disagree with, because the evidence was gone by the time
// they saw the document. A stamped signal has none of those failure
// modes: the caller decides, and IsPageChrome reproduces the old
// verdict exactly for a caller who wants it.
//
// The stamp is unconditional. Retention is lossless, so there is no
// configuration under which suppressing the signal is the better
// answer; any blocks the upstream Document.PageHeadersFooters returns
// were subtracted independently, and that leg is gone.
//
// What the fingerprint still cannot distinguish, which the stamp
// discloses rather than resolves: repetitive callout boxes ("Note:",
// "Warning:") with identical boilerplate across several pages carry a
// repeat count like a running header does. In practice their distinct
// body text below the marker keeps the fingerprint distinct.

package chunk

import (
	"strconv"
	"strings"
	"unicode"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/classify"
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

// Chrome signal metadata keys. Named as constants because the emitter,
// the accuracy harness, the wire-byte gate and any recipe preset that
// filters running chrome all have to agree with each other about the
// spelling.
const (
	// ChromeKeyPageRepeatCount is the number of DISTINCT pages the
	// block's fingerprint occurs on. Its spelling is defined in the
	// classify package, which also reads the key and cannot import this
	// one — the dependency points one way.
	ChromeKeyPageRepeatCount = classify.ChromeStampKey

	// ChromeKeyShape names the running-header form THIS occurrence
	// matched, or is absent when it matched none.
	ChromeKeyShape = "chrome_shape"

	// ChromeKeyRepeatShaped is a FINGERPRINT-level fact: whether ANY
	// occurrence of this fingerprint anywhere in the document matched
	// the running-header shape.
	ChromeKeyRepeatShaped = "chrome_repeat_shaped"
)

// ChromeSignalKeys lists every metadata key the chrome stamp writes.
// These ride only the blocks a repeated fingerprint applies to, unlike
// the nine always-on layout signals.
var ChromeSignalKeys = []string{
	ChromeKeyPageRepeatCount,
	ChromeKeyShape,
	ChromeKeyRepeatShaped,
}

// chromeShapePageNumberPipe is the value ChromeKeyShape takes for the
// "text pipe number" running-header form — the only shape the detector
// recognizes today.
const chromeShapePageNumberPipe = "page_number_pipe"

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
// fingerprint. Building the index up front lets stampRepeatedChrome
// stay flat — the stamping logic reads from this struct, as does
// dropSetFromChromeIndex, which retains the retired rule as the oracle
// the stamped predicate is proven against.
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

// dropSetFromChromeIndex returns the set of (page,blk) coordinates the
// RETIRED deletion rule would have removed. A fingerprint qualifies as
// chrome when its block count crosses the per-shape threshold; only
// entries matching the chrome shape are dropped (real headings on title
// pages share the fingerprint but aren't shaped).
//
// Nothing in production calls it any more — retention replaced
// deletion. It survives as the ORACLE the stamped predicate is proven
// against: IsPageChrome must select exactly these coordinates from the
// stamped metadata alone, and the equivalence test is what catches a
// stamp that loses one of the rule's three inputs.
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

// chromeStampMinPages is the repetition floor for STAMPING. It is
// deliberately lower than either drop threshold: the stamp is a
// disclosure, not a verdict, so anything that recurs at all is worth
// reporting and the consumer decides where to cut.
const chromeStampMinPages = 2

// stampRepeatedChrome walks the fingerprint index and writes the three
// chrome signals onto every block whose fingerprint recurs on
// chromeStampMinPages or more distinct pages. Blocks are MUTATED IN
// PLACE and nothing is removed.
//
// This replaced a rule that deleted those blocks outright, with no log
// and no counter. Deletion destroyed content that legitimately repeats
// verbatim exactly as thoroughly as it removed a running header, and a
// consumer who disagreed had nothing to disagree with. Retention is
// lossless: the same information is now a signal a recipe can filter
// on, and IsPageChrome below reproduces the old verdict for anyone who
// wants it.
func stampRepeatedChrome(perPage [][]layout.Block) {
	idx := indexChromeFingerprints(perPage)
	for f, pages := range idx.pages {
		if len(pages) < chromeStampMinPages {
			continue
		}
		count := strconv.Itoa(len(pages))
		shapedFP := idx.shapedFP[f]
		for _, e := range idx.entries[f] {
			if e.page < 0 || e.page >= len(perPage) || e.blk < 0 || e.blk >= len(perPage[e.page]) {
				continue
			}
			b := &perPage[e.page][e.blk]
			if b.Metadata == nil {
				b.Metadata = make(map[string]string, 3)
			}
			b.Metadata[ChromeKeyPageRepeatCount] = count
			b.Metadata[ChromeKeyRepeatShaped] = strconv.FormatBool(shapedFP)
			if e.shaped {
				b.Metadata[ChromeKeyShape] = chromeShapePageNumberPipe
			}
		}
	}
}

// IsPageChrome reports whether a block carrying the stamped chrome
// signals is what the retired deletion rule would have removed. It is
// the ONE copy of that rule: the accuracy harness and any other Go
// consumer call this rather than re-deriving the two thresholds, and a
// recipe expresses the same predicate over the same three keys.
//
// The rule reads:
//
//	count >= (repeat_shaped ? chromeMinPagesShaped : chromeMinPages)
//	AND (NOT repeat_shaped OR this occurrence is itself shaped)
//
// ALL THREE KEYS ARE LOAD-BEARING, which is why the fingerprint-level
// chrome_repeat_shaped exists alongside the per-occurrence
// chrome_shape. repeat_shaped selects the threshold — two pages for a
// shaped fingerprint, three otherwise — AND drives the sparing clause
// in the second line, which keeps a chapter title-page heading alive
// when its own text also runs as a header on later pages. A predicate
// over the count and the per-occurrence shape alone cannot express
// either, and measured on a real book it over-filters eighteen
// substantive headings while under-filtering twenty-seven running
// headers.
func IsPageChrome(md map[string]string) bool {
	if md == nil {
		return false
	}
	count, err := strconv.Atoi(md[ChromeKeyPageRepeatCount])
	if err != nil {
		return false
	}
	repeatShaped := md[ChromeKeyRepeatShaped] == "true"
	threshold := chromeMinPages
	if repeatShaped {
		threshold = chromeMinPagesShaped
	}
	if count < threshold {
		return false
	}
	return !repeatShaped || md[ChromeKeyShape] != ""
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
