// SPDX-License-Identifier: Apache-2.0

package web

import "time"

// Package-internal record types describing the structural output of the
// DOM walker. These records are the intermediate representation between
// the readability-cleaned article and the *knowledgev1.Node / kgwire.BatchEdge
// emission. They are NOT exported — the outward contract of this package
// is (raw HTML) → (*knowledgev1.Node, kgwire.BatchEdge) via collector.Collect.
//
// Every content record kind has a stable `recordKind()` string (used as
// the node Type after emission). Stable strings must never change
// once graphs are in production: they are part of the on-disk contract.

// pageRecord is one fetched + cleaned page. Sections are ordered top-level
// sections; nested sections appear as nestedSectionRecord inside parent
// section Children slices. InternalLinks are resolved absolute URLs that
// point back into the same site (for future cross-page resolution);
// ExternalCites are links to off-site resources recorded as their own
// linkRecord entries with rel="external".
type pageRecord struct {
	URL           string
	FinalURL      string
	Title         string
	Author        string
	PubDate       string
	Breadcrumb    []string
	FetchedAt     time.Time
	ContentHash   string // sha256 of raw body bytes (hex)
	HTTPStatus    int
	TopSections   []*sectionRecord
	InternalLinks []string // resolved absolute URLs, same-host as FinalURL
	ExternalCites []*linkRecord

	// Attrs holds verbatim DOM attributes from the page root; zero
	// interpretation — transformers decide what they mean.
	Attrs commonAttrs
}

// sectionRecord is a heading and the ordered content beneath it. Depth is
// the heading level (1-6). Anchor is the id attribute from the heading
// element, when present, so the emitted section node carries a stable
// in-page anchor.
type sectionRecord struct {
	Heading  string
	Depth    int
	Anchor   string
	Children []contentRecord

	// Attrs holds verbatim DOM attributes from the <section>/<article>
	// element; zero interpretation.
	Attrs commonAttrs
}

// contentRecord is the sealed interface satisfied by every in-section
// record kind. recordKind() returns the stable type string used as the
// node Type when the record is emitted.
type contentRecord interface {
	recordKind() string
}

// paragraphRecord is a run of inline prose. Inline <code> is preserved as
// backtick-wrapped text so the downstream translator can distinguish
// code-y tokens without needing the original DOM.
type paragraphRecord struct {
	Text string

	// Attrs holds verbatim DOM attributes from the <p> element; zero
	// interpretation.
	Attrs commonAttrs

	// InlineEmphasis lists inline <em>/<strong>/<code>/... spans within
	// the paragraph. Zero-valued nil means none recorded.
	InlineEmphasis []inlineEmphasis
}

func (paragraphRecord) recordKind() string { return "paragraph" }

// inlineEmphasis is one inline-emphasis span inside a paragraphRecord,
// listItemRecord, or quoteRecord. Tag is the element name ("em",
// "strong", "code", "b", "i", "kbd"). Text is the span's normalized
// text content (whitespace-collapsed). Position is the zero-based
// character offset into the enclosing record's flattened Text at which
// the span begins. JSON tags are explicit lowercase so the emitted
// metadata list uses stable on-disk keys.
type inlineEmphasis struct {
	Tag      string `json:"tag"`
	Text     string `json:"text"`
	Position int    `json:"position"`
}

// codeBlockRecord is a <pre>/<pre><code> block. Language is recovered
// from a `language-xxx` or `lang-xxx` class attribute when present;
// AttrHint retains any other class-attribute hint (for example "highlight")
// that the downstream translator might find useful.
type codeBlockRecord struct {
	Language string
	Source   string
	AttrHint string

	// Attrs holds verbatim DOM attributes from the <pre>/<code> element;
	// zero interpretation.
	Attrs commonAttrs
}

func (codeBlockRecord) recordKind() string { return "code_block" }

// listItemRecord is one <li>/<dd> entry within a listRecord. Position is
// the zero-based index within the containing list; it is preserved here
// so the emitter can propagate it onto the contains-edge metadata.
type listItemRecord struct {
	Text     string
	Position int

	// Attrs holds verbatim DOM attributes from the <li>/<dd> element;
	// zero interpretation.
	Attrs commonAttrs

	// InlineEmphasis lists inline <em>/<strong>/<code>/... spans within
	// the list item. Zero-valued nil means none recorded.
	InlineEmphasis []inlineEmphasis
}

func (listItemRecord) recordKind() string { return "list_item" }

// listRecord groups listItemRecord entries. Ordered is true for <ol>,
// false for <ul>/<dl>. Kind is "ul", "ol", or "dl" so the downstream
// consumer can distinguish description lists from bullet / numeric lists.
type listRecord struct {
	Ordered bool
	Kind    string
	Items   []listItemRecord

	// Attrs holds verbatim DOM attributes from the <ul>/<ol>/<dl>
	// element; zero interpretation.
	Attrs commonAttrs
}

func (listRecord) recordKind() string { return "list" }

// tableRecord is a simple two-dimensional table. Headers is the first row
// or the contents of <thead>; Rows is the remaining body rows. Cell text
// is collapsed to a single line of whitespace-normalized text per cell.
type tableRecord struct {
	Headers []string
	Rows    [][]string

	// Attrs holds verbatim DOM attributes from the <table> element;
	// zero interpretation.
	Attrs commonAttrs
}

func (tableRecord) recordKind() string { return "table" }

// linkRecord is a single <a href>. URL is resolved absolute. Rel is the
// classified relationship ("internal" or "external") — the walker sets
// this based on same-hostname comparison against the containing page's
// FinalURL. Anchor is the fragment portion of URL, stripped into its own
// field for convenience. NoFollow is true when the <a>'s rel attribute
// contained a "nofollow" token (case-insensitive); such links are still
// emitted as reference nodes but are excluded from pageRecord.InternalLinks
// so the crawler's BFS never enqueues them.
type linkRecord struct {
	URL      string
	Text     string
	Rel      string
	Anchor   string
	NoFollow bool

	// Attrs holds verbatim DOM attributes from the <a> element; zero
	// interpretation.
	Attrs commonAttrs
}

func (linkRecord) recordKind() string { return "link" }

// imageRecord is a <img> element plus an optional <figcaption>. URL is
// resolved absolute.
type imageRecord struct {
	URL     string
	Alt     string
	Caption string

	// Attrs holds verbatim DOM attributes from the <img>/<figure>
	// element; zero interpretation.
	Attrs commonAttrs
}

func (imageRecord) recordKind() string { return "image" }

// quoteRecord is a <blockquote>. CiteURL is the resolved absolute URL
// from the <blockquote cite=...> attribute, when present.
type quoteRecord struct {
	Text    string
	CiteURL string

	// Attrs holds verbatim DOM attributes from the <blockquote> element;
	// zero interpretation.
	Attrs commonAttrs

	// InlineEmphasis lists inline <em>/<strong>/<code>/... spans within
	// the blockquote. Zero-valued nil means none recorded.
	InlineEmphasis []inlineEmphasis
}

func (quoteRecord) recordKind() string { return "blockquote" }

// nestedSectionRecord wraps a *sectionRecord so a deeper heading can be
// attached as a child of its parent section's Children slice while still
// satisfying the contentRecord interface. The recordKind is "section" so
// emitted nodes carry that type.
type nestedSectionRecord struct {
	Section *sectionRecord
}

func (nestedSectionRecord) recordKind() string { return "section" }
