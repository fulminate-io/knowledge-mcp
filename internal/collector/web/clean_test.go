// SPDX-License-Identifier: Apache-2.0

package web

import (
	"bytes"
	"log/slog"
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

// TestCleanArticleHohpeTitleAndChromeStrip covers what readability still
// contributes after the extracted-HTML view was removed: the title. The
// chrome-strip assertions that used to read the cleaned HTML are gone with
// that field — chrome exclusion is now the DOM walker's job, and is fenced by
// the walker's own tests against the raw bytes.
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
}

func TestCleanArticleMalformedFallsBackGracefully(t *testing.T) {
	t.Parallel()
	raw := loadFixture(t, "malformed.html")

	// Malformed HTML must not panic and must return a non-nil *cleanedArticle
	// with a nil error — the page is captured either way, because the DOM
	// walker reads the raw bytes rather than anything produced here.
	got, err := cleanArticle(raw, "https://example.com/malformed")
	if err != nil {
		t.Fatalf("cleanArticle on malformed: unexpected err %v", err)
	}
	if got == nil {
		t.Fatal("cleanArticle: nil result on malformed HTML")
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

	// Invalid source URL: we still want a cleanedArticle back, empty.
	got, err := cleanArticle(raw, "not-a-valid-url")
	if err != nil {
		t.Fatalf("cleanArticle: %v", err)
	}
	if got == nil {
		t.Fatal("cleanArticle: nil result with bad URL")
	}
	// Nothing was extracted, so every surviving field is empty. Compared
	// against the good-URL run below, which populates Title from the same
	// fixture — without that control this would pass on a cleanArticle that
	// extracted nothing from anything.
	if got.Title != "" || got.Byline != "" || got.PubDate != "" {
		t.Errorf("bad-url fallback should extract nothing, got title=%q byline=%q pub_date=%q",
			got.Title, got.Byline, got.PubDate)
	}
	ok, err := cleanArticle(raw, "https://www.enterpriseintegrationpatterns.com/patterns/messaging/MessageChannel.html")
	if err != nil {
		t.Fatalf("control: cleanArticle: %v", err)
	}
	if ok.Title == "" {
		t.Error("control: the same fixture with a valid URL extracted no title — the emptiness above proves nothing")
	}
}

// TestCleanArticle_FailureIsLoud pins the audibility of the readability
// failure lane, and pins that its CONTROL FLOW is unchanged.
//
// Warn-and-continue here is an expressly approved decision, so this test does
// NOT assert a hard failure — it asserts the degradation is announced rather
// than silent: a WARN record naming the URL and stating that title, byline and
// pub_date are unavailable, while cleanArticle still returns a non-nil article
// and a nil error so the page is still captured.
func TestCleanArticle_FailureIsLoud(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	const badURL = "not-a-valid-url-at-all"
	got, err := cleanArticle([]byte("<html><body><h1>Some Page</h1></body></html>"), badURL)

	// CONTROL FLOW IS UNCHANGED: still captured, still no error.
	if err != nil {
		t.Fatalf("cleanArticle returned an error %v — the lane must stay warn-and-continue", err)
	}
	if got == nil {
		t.Fatal("cleanArticle returned nil — the lane must stay warn-and-continue")
	}

	logged := buf.String()
	if !strings.Contains(logged, "level=WARN") {
		t.Errorf("readability failure was not logged at WARN\n--- log ---\n%s", logged)
	}
	if !strings.Contains(logged, badURL) {
		t.Errorf("the warning does not name the source URL %q\n--- log ---\n%s", badURL, logged)
	}
	for _, lost := range []string{"title", "byline", "pub_date"} {
		if !strings.Contains(logged, lost) {
			t.Errorf("the warning does not name %q among what was lost\n--- log ---\n%s", lost, logged)
		}
	}

	// THE OTHER RAISED LANE: readability itself failing to parse. Both lanes
	// were raised from Debug to Warn, so both need an assertion; malformed.html
	// reaches this one.
	buf.Reset()
	const malformedURL = "https://example.com/malformed"
	got2, err := cleanArticle(loadFixture(t, "malformed.html"), malformedURL)
	if err != nil || got2 == nil {
		t.Fatalf("readability-failure lane must stay warn-and-continue, got article=%v err=%v", got2, err)
	}
	logged = buf.String()
	if !strings.Contains(logged, "level=WARN") || !strings.Contains(logged, malformedURL) {
		t.Errorf("readability parse failure was not announced at WARN naming %q\n--- log ---\n%s", malformedURL, logged)
	}

	// KNOWN NEGATIVE, same handler and same buffer: a SUCCESSFUL clean emits
	// no warning at all. Without it, a cleanArticle that warned on every page
	// would pass everything above.
	buf.Reset()
	if _, err := cleanArticle(loadFixture(t, "hohpe_sample.html"),
		"https://www.enterpriseintegrationpatterns.com/patterns/messaging/MessageChannel.html"); err != nil {
		t.Fatalf("control: cleanArticle: %v", err)
	}
	if strings.Contains(buf.String(), "level=WARN") {
		t.Errorf("a successful clean emitted a WARN — the warning is not specific to the failure lane\n--- log ---\n%s", buf.String())
	}
}
