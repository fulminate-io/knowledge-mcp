package classify

import "regexp"

// ClassifyParams tunes the heading / list / code / caption classifier.
// T1 pinned the 4-field surface; T7 fills DefaultClassifyParams below
// with the production-default ratios and list-marker regex.
type ClassifyParams struct {
	// HeadingFontSizeRatio is the minimum ratio of a candidate-heading
	// block's font size to the document body font size for the block
	// to be classified as a heading. e.g. 1.2 means "≥ 20% larger
	// than body text".
	HeadingFontSizeRatio float64

	// HeadingMinBoldOnly, when true, gates Rule #1 (size-only path) on
	// boldness: a block that is ≥ HeadingFontSizeRatio× body size but
	// not bold is NOT a heading-candidate from size alone. Rule #2
	// (all-bold at body size) and Rule #3 (short block + vertical gap)
	// remain available regardless of this flag. Reduces false-positive
	// heading classification of pull-quotes, captions, and other large
	// non-bold body text.
	HeadingMinBoldOnly bool

	// CodeMonospaceRatio is the minimum fraction (0..1) of a block's
	// glyphs that must be in a monospace font for the block to be
	// classified as code. e.g. 0.8 = 80% of glyphs in monospace.
	CodeMonospaceRatio float64

	// ListMarkerPattern is the regexp matched against the start of a
	// candidate-list-item line. Match → BlockListItem. Authors choose
	// patterns such as `^\s*[-•*]\s+` (bulleted) or
	// `^\s*\d+[.)]\s+` (numbered). Nil disables list-item detection.
	ListMarkerPattern *regexp.Regexp
}

// defaultListMarkerPattern is the v1 list-marker regex compiled once
// at package init. Matches:
//   - bullets: • ◦ ▪ – — *
//   - decimal-numbered: 1. or (1) or 1)
//   - alphabetic: a. or (a) or a) (single-letter, any case)
//   - lower/upper roman: i. ii. iii. iv. v. ... I. II. III. ...
//
// `^\s*` (zero-or-more leading whitespace) is intentional: column-0
// markers at the start of a line must match. Raw-string literal so
// the regex source reads verbatim — no double-backslash escapes.
// Non-Latin scripts (Arabic, CJK enumerators) are deferred to v2.
var defaultListMarkerPattern = regexp.MustCompile(`^\s*([•◦▪–—*]|\(?\d+[.)]|\(?[a-zA-Z][.)]|[ivxIVX]+\.)\s+`)

// DefaultClassifyParams holds the v1 production-default ratios and
// list-marker regex. Used by classify.Classify (the no-params public
// entry point); ClassifyWithParams accepts caller-supplied overrides.
var DefaultClassifyParams = ClassifyParams{
	HeadingFontSizeRatio: 1.15,
	HeadingMinBoldOnly:   true,
	CodeMonospaceRatio:   0.8,
	ListMarkerPattern:    defaultListMarkerPattern,
}
