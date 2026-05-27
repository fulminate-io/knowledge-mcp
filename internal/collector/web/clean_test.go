// SPDX-License-Identifier: Apache-2.0

package web

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func loadFixture(t *testing.T, name string) []byte {
	t.Helper()
	path := filepath.Join("testdata", name)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture %s: %v", path, err)
	}
	return b
}

func TestCleanArticleHohpeTitleAndChromeStrip(t *testing.T) {
	t.Parallel()
	raw := loadFixture(t, "hohpe_sample.html")

	got, err := cleanArticle(raw, "https://www.enterpriseintegrationpatterns.com/patterns/messaging/MessageChannel.html")
	if err != nil {
		t.Fatalf("cleanArticle: %v", err)
	}
	if got == nil {
		t.Fatal("cleanArticle: nil result")
	}
	if !strings.Contains(got.Title, "Message Channel") {
		t.Errorf("title missing 'Message Channel': got %q", got.Title)
	}

	cleanedStr := string(got.CleanedHTML)
	// Chrome must be stripped: nav, sidebar, and footer chrome should be gone
	// (or at least not surfaced alongside the article body the way readability
	// returns it). The copyright-year footer text is the strongest "chrome"
	// marker in the fixture.
	if strings.Contains(cleanedStr, "Copyright 2003-2024") {
		t.Errorf("chrome not stripped: cleaned HTML contains footer copyright")
	}
	if strings.Contains(cleanedStr, "console.log") {
		t.Errorf("chrome not stripped: cleaned HTML contains <script>")
	}

	// Core article content must survive.
	if !strings.Contains(cleanedStr, "Message Channel") {
		t.Errorf("article body lost: %q", cleanedStr)
	}
	if got.TextLen == 0 {
		t.Errorf("TextLen should be > 0 for a non-empty article")
	}
}

func TestCleanArticleGo101TitleAndContent(t *testing.T) {
	t.Parallel()
	raw := loadFixture(t, "go101_sample.html")

	got, err := cleanArticle(raw, "https://go101.org/article/channel.html")
	if err != nil {
		t.Fatalf("cleanArticle: %v", err)
	}
	if got == nil {
		t.Fatal("cleanArticle: nil result")
	}
	if !strings.Contains(got.Title, "Channels in Go") {
		t.Errorf("title missing 'Channels in Go': got %q", got.Title)
	}

	cleanedStr := string(got.CleanedHTML)
	// Article code/prose should survive chrome strip.
	if !strings.Contains(cleanedStr, "select") {
		t.Errorf("article body lost select reference: %q", cleanedStr)
	}
	// Topnav should be stripped.
	if strings.Contains(cleanedStr, "topnav") {
		t.Errorf("topnav chrome not stripped")
	}
}

func TestCleanArticleMalformedFallsBackGracefully(t *testing.T) {
	t.Parallel()
	raw := loadFixture(t, "malformed.html")

	// Malformed HTML must not panic and must return *cleanedArticle — either
	// a best-effort readability result or the raw-HTML fallback.
	got, err := cleanArticle(raw, "https://example.com/malformed")
	if err != nil {
		t.Fatalf("cleanArticle on malformed: unexpected err %v", err)
	}
	if got == nil {
		t.Fatal("cleanArticle: nil result on malformed HTML")
	}
	if len(got.CleanedHTML) == 0 {
		t.Errorf("CleanedHTML should be non-empty (fallback returns raw)")
	}
}

func TestCleanArticleEmptyReturnsError(t *testing.T) {
	t.Parallel()
	got, err := cleanArticle(nil, "https://example.com/")
	if err == nil {
		t.Fatalf("expected error on empty rawHTML, got %+v", got)
	}
	if got != nil {
		t.Errorf("expected nil result on empty rawHTML")
	}
}

func TestCleanArticleBadSourceURLFallsBack(t *testing.T) {
	t.Parallel()
	raw := loadFixture(t, "hohpe_sample.html")

	// Invalid source URL: we still want a cleanedArticle back, via raw fallback.
	got, err := cleanArticle(raw, "not-a-valid-url")
	if err != nil {
		t.Fatalf("cleanArticle: %v", err)
	}
	if got == nil {
		t.Fatal("cleanArticle: nil result with bad URL")
	}
	// Fallback path: CleanedHTML should equal raw.
	if string(got.CleanedHTML) != string(raw) {
		t.Errorf("bad-url fallback should return raw HTML as CleanedHTML")
	}
}
