// SPDX-License-Identifier: Apache-2.0

package web

import (
	"bytes"
	"fmt"
	"log/slog"
	"net/url"
	"time"

	readability "codeberg.org/readeck/go-readability/v2"
)

// cleanedArticle is what readability contributes to a page record: the
// article's title, byline and publication date, and nothing else. The DOM
// walker parses the RAW response bytes, not readability's cleaned tree (see
// parsePage), so no extracted-HTML view is carried here.
type cleanedArticle struct {
	Title   string
	Byline  string
	PubDate string // RFC3339 string, empty when not present
}

// cleanArticle runs go-readability (readeck/v2 fork) over rawHTML and returns
// a cleanedArticle with title / byline / publication date extracted. Only
// returns error when rawHTML is empty — every non-empty page gets a
// cleanedArticle.
//
// WARN-AND-CONTINUE ON A READABILITY FAILURE IS A DELIBERATE, EXPRESSLY
// APPROVED DECISION, not an implementer's default. Hard-failing the page was
// considered and rejected on these grounds: the full response HTML is retained
// regardless, pickTitle falls back to the document's first H1, and only
// Title/Byline/PubDate enrichment is at stake — so dropping the page would
// lose far more than it protects. Both lanes below therefore warn loudly,
// naming the URL and what was lost, and return an empty cleanedArticle so the
// page is still captured. Do not silently downgrade these back to Debug, and
// do not convert them to hard failures without the same explicit approval.
func cleanArticle(rawHTML []byte, sourceURL string) (*cleanedArticle, error) {
	if len(rawHTML) == 0 {
		return nil, fmt.Errorf("clean: empty rawHTML")
	}

	parsedURL, urlErr := url.Parse(sourceURL)
	if urlErr != nil || parsedURL == nil || parsedURL.Host == "" {
		slog.Warn("web.clean: invalid sourceURL — title, byline and pub_date unavailable for this page; title falls back to its first H1",
			"url", sourceURL, "err", urlErr)
		return rawFallback(), nil
	}

	article, err := readability.FromReader(bytes.NewReader(rawHTML), parsedURL)
	if err != nil || article.Node == nil {
		slog.Warn("web.clean: readability parse failed — title, byline and pub_date unavailable for this page; title falls back to its first H1",
			"url", sourceURL, "err", err)
		return rawFallback(), nil
	}

	return &cleanedArticle{
		Title:   article.Title(),
		Byline:  article.Byline(),
		PubDate: pubDateString(article),
	}, nil
}

// rawFallback is what a page gets when readability cannot contribute
// anything: an EMPTY cleanedArticle. The page itself is still captured — the
// DOM walker parses the raw response bytes rather than any readability output,
// and pickTitle falls back to the document's first H1.
func rawFallback() *cleanedArticle {
	return &cleanedArticle{}
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
