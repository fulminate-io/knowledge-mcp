// SPDX-License-Identifier: Apache-2.0

package web

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
)

// captureHandler collects slog records so a test can assert on ATTRIBUTE
// VALUES rather than on rendered output.
//
// THE DISTINCTION IS THE WHOLE INSTRUMENT. slog's own TextHandler and
// JSONHandler quote a string attribute that contains a newline, so a test
// that asserted over rendered text would pass whether or not the value was
// sanitized and would prove nothing about the value. What this class of
// defect is about is the VALUE reaching a line-oriented sink intact, so the
// assertion is made on the value the handler is handed, before any handler
// decides whether to escape it.
type captureHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *captureHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r.Clone())
	return nil
}

func (h *captureHandler) WithAttrs([]slog.Attr) slog.Handler { return h }

func (h *captureHandler) WithGroup(string) slog.Handler { return h }

// stringAttrs flattens every captured record into "<key>=<value>" pairs for
// the string-valued attributes, which are the ones a forged newline can ride.
func (h *captureHandler) stringAttrs() map[string]string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := map[string]string{}
	for _, r := range h.records {
		r.Attrs(func(a slog.Attr) bool {
			out[a.Key] = a.Value.String()
			return true
		})
		out["__msg__"] = r.Message
	}
	return out
}

// captureLogs installs a capturing handler as the default slog logger for the
// duration of the test and returns it.
func captureLogs(t *testing.T) *captureHandler {
	t.Helper()
	h := &captureHandler{}
	prior := slog.Default()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(prior) })
	return h
}

// assertNoRecordBreak fails when any captured string attribute carries a raw
// carriage return or line feed — that is a value able to end its own log
// record and open a second, attacker-authored one.
func assertNoRecordBreak(t *testing.T, h *captureHandler) {
	t.Helper()
	if len(h.records) == 0 {
		t.Fatalf("no log records captured — the site under test did not log, so this assertion could not fail")
	}
	for k, v := range h.stringAttrs() {
		if strings.ContainsAny(v, "\r\n") {
			t.Errorf("attribute %q carries a raw record break into a line-oriented sink: %q", k, v)
		}
	}
}

// forgedURL is one line of URL followed by a forged second log record.
const forgedURL = "https://example.com/a\nlevel=INFO msg=\"forged record\""

func TestCleanArticle_InvalidURLLogIsNewlineSafe(t *testing.T) {
	h := captureLogs(t)
	if _, err := cleanArticle([]byte("<html><body><p>x</p></body></html>"), forgedURL); err != nil {
		t.Fatalf("cleanArticle: %v", err)
	}
	assertNoRecordBreak(t, h)
}

// TestEnqueueDiscovered_ARecordBreakingURLNeverReachesTheQueue pins the reason
// the path-segment-cap log site (crawl.go, "web.crawl: path-segment cap dropped
// URL") cannot be reached with a record-breaking URL today: every branch above
// it requires url.Parse to have succeeded, and Go's url.Parse rejects ASCII
// control characters, so `raw` there has already been proven newline-free.
//
// IT IS HERE AS A DETECTOR, NOT AS COVERAGE. The site is sanitized anyway. If
// canonicalizeURL ever starts admitting a control character, this test reds and
// names the log site that then becomes reachable.
func TestEnqueueDiscovered_ARecordBreakingURLNeverReachesTheQueue(t *testing.T) {
	h := captureLogs(t)
	s := newCrawlState(CrawlOptions{MaxPathSegments: 1}, nil)
	forged := "https://example.com/a/b/c\nlevel=INFO msg=\"forged record\""
	s.enqueueDiscovered(forged, 0)
	if len(s.queue) != 0 {
		t.Errorf("a URL carrying a record break was queued (%q); the path-segment-cap log site is now reachable with it and its sanitization is load-bearing", s.queue[0].url)
	}
	if got := normalizeURL(forged); got != "" {
		t.Errorf("normalizeURL admitted a control character: %q", got)
	}
	// A URL that DOES parse still reaches the cap branch, which is the
	// known-positive proving the branch is live rather than dead.
	s.enqueueDiscovered("https://example.com/a/b/c", 0)
	if len(h.records) == 0 {
		t.Fatalf("the path-segment cap branch did not log for a well-formed over-long URL — this test's control is broken")
	}
	assertNoRecordBreak(t, h)
}

func TestIsContentAlias_LogIsNewlineSafe(t *testing.T) {
	h := captureLogs(t)
	s := newCrawlState(CrawlOptions{}, nil)
	s.hashToURL["deadbeef"] = "https://example.com/first\nlevel=INFO msg=\"forged alias\""
	s.isContentAlias(&pageRecord{URL: forgedURL, ContentHash: "deadbeef"})
	assertNoRecordBreak(t, h)
}

func TestHostCapReached_LogIsNewlineSafe(t *testing.T) {
	h := captureLogs(t)
	s := newCrawlState(CrawlOptions{MaxPagesPerHost: 1}, nil)
	// The host is derived from FinalURL — recordHost falls through to it when
	// URL does not parse — so record.URL is free to carry the forged break,
	// which is exactly the value the cap log line writes.
	rec := &pageRecord{URL: forgedURL, FinalURL: "https://example.com/b"}
	host := recordHost(rec)
	if host == "" {
		t.Fatalf("recordHost returned empty for %q — the cap branch cannot be reached", rec.URL)
	}
	s.hostPageCount[host] = 1
	if !s.hostCapReached(rec) {
		t.Fatalf("hostCapReached=false — the logging branch under test did not run")
	}
	assertNoRecordBreak(t, h)
}

func TestLogSafe(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain is untouched", "https://example.com/a", "https://example.com/a"},
		{"empty is untouched", "", ""},
		{"line feed is escaped", "a\nb", `a\nb`},
		{"carriage return is escaped", "a\rb", `a\rb`},
		{"crlf is escaped as two escapes", "a\r\nb", `a\r\nb`},
		{"every break is escaped, not just the first", "a\nb\nc\rd", `a\nb\nc\rd`},
		{"other control characters are left alone", "a\tb", "a\tb"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := logSafe(tc.in); got != tc.want {
				t.Errorf("logSafe(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestLogSafeErr(t *testing.T) {
	if got := logSafeErr(nil); got != "<nil>" {
		t.Errorf("logSafeErr(nil) = %q, want %q — a nil error must render as slog renders it", got, "<nil>")
	}
	if got := logSafeErr(errors.New("boom\nlevel=INFO msg=forged")); got != `boom\nlevel=INFO msg=forged` {
		t.Errorf("logSafeErr = %q, want the break escaped", got)
	}
}
