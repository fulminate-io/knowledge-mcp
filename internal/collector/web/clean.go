// SPDX-License-Identifier: Apache-2.0

package web

import (
	"bytes"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	readability "codeberg.org/readeck/go-readability/v2"
	"codeberg.org/readeck/go-readability/v2/render"
)

// cleanedArticle is the post-chrome-strip view of a page. CleanedHTML carries
// the readability-extracted content as HTML bytes; TextLen is the length of
// the visible text (whitespace-collapsed). When readability fails, the
// wrapper falls back to the raw HTML so the DOM walker in Phase 3 still has
// something to chew on — we capture every page, even ones readability can't
// parse.
type cleanedArticle struct {
	Title       string
	Byline      string
	SiteName    string
	PubDate     string // RFC3339 string, empty when not present
	CleanedHTML []byte
	TextLen     int
}

// cleanArticle runs go-readability (readeck/v2 fork) over rawHTML and returns
// a cleanedArticle with title / byline / site-name / cleaned-HTML extracted.
// If sourceURL fails to parse or readability errors, the function logs at
// debug and falls through to a best-effort cleanedArticle whose CleanedHTML
// is the raw bytes. Only returns error when rawHTML is empty — every
// non-empty page gets a cleanedArticle.
func cleanArticle(rawHTML []byte, sourceURL string) (*cleanedArticle, error) {
	if len(rawHTML) == 0 {
		return nil, fmt.Errorf("clean: empty rawHTML")
	}

	parsedURL, urlErr := url.Parse(sourceURL)
	if urlErr != nil || parsedURL == nil || parsedURL.Host == "" {
		slog.Debug("web.clean: invalid sourceURL, falling back to raw",
			"url", sourceURL, "err", urlErr)
		return rawFallback(rawHTML), nil
	}

	article, err := readability.FromReader(bytes.NewReader(rawHTML), parsedURL)
	if err != nil || article.Node == nil {
		slog.Debug("web.clean: readability parse failed, falling back to raw",
			"url", sourceURL, "err", err)
		return rawFallback(rawHTML), nil
	}

	cleanedHTML, renderErr := renderArticleHTML(article)
	if renderErr != nil {
		slog.Debug("web.clean: render failed, falling back to raw",
			"url", sourceURL, "err", renderErr)
		return rawFallback(rawHTML), nil
	}

	return &cleanedArticle{
		Title:       article.Title(),
		Byline:      article.Byline(),
		SiteName:    article.SiteName(),
		PubDate:     pubDateString(article),
		CleanedHTML: cleanedHTML,
		TextLen:     articleTextLen(article),
	}, nil
}

// rawFallback constructs a cleanedArticle from the raw HTML when readability
// cannot produce a result. Title/Byline/SiteName are empty — Phase 3's DOM
// walker will still process the raw bytes.
func rawFallback(rawHTML []byte) *cleanedArticle {
	return &cleanedArticle{
		CleanedHTML: rawHTML,
		TextLen:     visibleTextLen(rawHTML),
	}
}

// renderArticleHTML writes the readability Node tree to HTML bytes.
func renderArticleHTML(article readability.Article) ([]byte, error) {
	var buf bytes.Buffer
	if err := article.RenderHTML(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// pubDateString returns the published time as RFC3339, or "" when the
// document had no timestamp metadata. Parse errors downgrade to "".
func pubDateString(article readability.Article) string {
	t, err := article.PublishedTime()
	if err != nil {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// articleTextLen returns the length of the visible text in the article's
// node tree (whitespace-collapsed).
func articleTextLen(article readability.Article) int {
	if article.Node == nil {
		return 0
	}
	var buf bytes.Buffer
	if err := article.RenderText(&buf); err != nil {
		return 0
	}
	return len(strings.Join(strings.Fields(buf.String()), " "))
}

// visibleTextLen approximates the visible-text length of raw HTML by using
// go-readability's render helpers on a cheap parse. A byte-count fallback is
// returned when parsing fails — any non-zero signal is better than zero.
func visibleTextLen(rawHTML []byte) int {
	// Cheap: trim tags via render.InnerText after a permissive parse through
	// readability. We don't care about accuracy here — Phase 3 is authoritative.
	if len(rawHTML) == 0 {
		return 0
	}
	// readability.FromReader can fail on fragments; in that case we just
	// report byte length as a coarse upper bound.
	article, err := readability.FromReader(bytes.NewReader(rawHTML), nil)
	if err != nil || article.Node == nil {
		return len(bytes.TrimSpace(rawHTML))
	}
	text := render.InnerText(article.Node)
	return len(strings.Join(strings.Fields(text), " "))
}
