// SPDX-License-Identifier: Apache-2.0

package web

import (
	"context"
	"crypto/sha256"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestFetchHappyPath(t *testing.T) {
	t.Parallel()
	var gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("<html><body>hello</body></html>"))
	}))
	defer srv.Close()

	c := newFetchClient("", 0)
	page, err := c.fetch(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if page.Status != 200 {
		t.Fatalf("status: got %d want 200", page.Status)
	}
	if !strings.Contains(string(page.Body), "hello") {
		t.Fatalf("body missing 'hello': %q", page.Body)
	}
	if page.FinalURL != srv.URL {
		t.Fatalf("final url: got %q want %q", page.FinalURL, srv.URL)
	}
	if gotUA != defaultUserAgent {
		t.Fatalf("user-agent: got %q want %q", gotUA, defaultUserAgent)
	}
	if page.FetchedAt.IsZero() {
		t.Fatal("FetchedAt not set")
	}
	if page.Header.Get("Content-Type") == "" {
		t.Fatal("header Content-Type missing")
	}
}

func TestFetchNon2xxReturnsPage(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		code int
	}{
		{"404 not found", http.StatusNotFound},
		{"500 server error", http.StatusInternalServerError},
		{"403 forbidden", http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.code)
				_, _ = w.Write([]byte("body"))
			}))
			defer srv.Close()

			c := newFetchClient("test/0.1", 0)
			page, err := c.fetch(context.Background(), srv.URL)
			if err != nil {
				t.Fatalf("fetch: unexpected err %v", err)
			}
			if page == nil {
				t.Fatal("page: got nil, want non-nil for non-2xx")
			}
			if page.Status != tc.code {
				t.Fatalf("status: got %d want %d", page.Status, tc.code)
			}
		})
	}
}

func TestFetchRedirectFollow(t *testing.T) {
	t.Parallel()
	var finalHit bool
	mux := http.NewServeMux()
	mux.HandleFunc("/final", func(w http.ResponseWriter, r *http.Request) {
		finalHit = true
		_, _ = w.Write([]byte("final"))
	})
	mux.HandleFunc("/r1", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/r2", http.StatusFound)
	})
	mux.HandleFunc("/r2", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/final", http.StatusFound)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := newFetchClient("", 0)
	page, err := c.fetch(context.Background(), srv.URL+"/r1")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if !finalHit {
		t.Fatal("final handler was not reached")
	}
	if page.Status != 200 {
		t.Fatalf("status: got %d want 200", page.Status)
	}
	if !strings.HasSuffix(page.FinalURL, "/final") {
		t.Fatalf("FinalURL: got %q, want suffix /final", page.FinalURL)
	}
	if page.URL != srv.URL+"/r1" {
		t.Fatalf("URL: got %q want %q", page.URL, srv.URL+"/r1")
	}
}

func TestFetchRedirectLoopHalts(t *testing.T) {
	t.Parallel()
	var hits atomic.Int32
	mux := http.NewServeMux()
	// /loop redirects to /loop — unbounded unless capped.
	mux.HandleFunc("/loop", func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Redirect(w, r, "/loop", http.StatusFound)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := newFetchClient("", 0)
	page, err := c.fetch(context.Background(), srv.URL+"/loop")
	// When CheckRedirect returns ErrUseLastResponse, http.Client returns the
	// last response without error. Either outcome is acceptable here; what
	// we care about is that we did not loop forever.
	if err != nil {
		t.Logf("got error (acceptable): %v", err)
	}
	if page != nil {
		if page.Status != http.StatusFound {
			t.Logf("status: %d (acceptable — we halted)", page.Status)
		}
	}
	// Guard against runaway: must be <= maxRedirects + 1 (the original
	// request counts as hit #1, then up to maxRedirects more).
	total := hits.Load()
	if int(total) > maxRedirects+1 {
		t.Fatalf("redirect loop not halted: got %d hits (max allowed %d)",
			total, maxRedirects+1)
	}
}

func TestFetchContextCancellation(t *testing.T) {
	t.Parallel()
	started := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		// Block longer than the context deadline.
		select {
		case <-r.Context().Done():
		case <-time.After(3 * time.Second):
		}
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-started
		cancel()
	}()

	c := newFetchClient("", 0)
	page, err := c.fetch(ctx, srv.URL)
	if err == nil {
		t.Fatalf("expected error on cancellation, got page=%+v", page)
	}
	if page != nil {
		t.Fatalf("expected nil page on cancellation, got %+v", page)
	}
}

func TestFetchPolitenessSameHost(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	const politenessMs = 150
	c := newFetchClient("", politenessMs)

	start := time.Now()
	for i := range 3 {
		if _, err := c.fetch(context.Background(), srv.URL); err != nil {
			t.Fatalf("fetch %d: %v", i, err)
		}
	}
	elapsed := time.Since(start)
	// Two politeness gaps between three requests. Allow 80% margin for flake.
	expected := time.Duration(2*politenessMs) * time.Millisecond
	minAllowed := expected * 80 / 100
	if elapsed < minAllowed {
		t.Fatalf("politeness not enforced: elapsed=%v want >= %v", elapsed, minAllowed)
	}
}

func TestFetchPolitenessDifferentHostsIndependent(t *testing.T) {
	t.Parallel()
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	a := httptest.NewServer(h)
	defer a.Close()
	b := httptest.NewServer(h)
	defer b.Close()

	const politenessMs = 200
	c := newFetchClient("", politenessMs)

	start := time.Now()
	if _, err := c.fetch(context.Background(), a.URL); err != nil {
		t.Fatalf("fetch a: %v", err)
	}
	if _, err := c.fetch(context.Background(), b.URL); err != nil {
		t.Fatalf("fetch b: %v", err)
	}
	elapsed := time.Since(start)
	if elapsed > time.Duration(politenessMs)*time.Millisecond {
		t.Fatalf("different-host fetches blocked on politeness: elapsed=%v", elapsed)
	}
}

func TestFetchInvalidURL(t *testing.T) {
	t.Parallel()
	c := newFetchClient("", 0)
	cases := []string{
		"not-a-url",
		"ftp://example.com/",
		"http:///no-host",
	}
	for _, raw := range cases {
		t.Run(raw, func(t *testing.T) {
			t.Parallel()
			page, err := c.fetch(context.Background(), raw)
			if err == nil {
				t.Fatalf("expected error for %q, got page=%+v", raw, page)
			}
			if page != nil {
				t.Fatalf("expected nil page for invalid url %q", raw)
			}
		})
	}
}

func TestFetchTimeout(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	defer srv.Close()

	c := newFetchClient("", 0)
	// Shrink client timeout just for this test.
	c.httpClient.Timeout = 100 * time.Millisecond

	_, err := c.fetch(context.Background(), srv.URL)
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
}

// TestFetch_LargeBodyLandsComplete is the fence on the removed page-body cap.
//
// The deleted mechanism read the body through a bounded reader at a fixed
// ceiling and reported the truncated result as SUCCESS — measured against the
// unfixed tree, a 10485760-byte prefix with a sha256 that does not match the
// served bytes. This test drives a body strictly larger than that ceiling and
// asserts the fetched body is byte-complete and hash-identical, so a
// reintroduced ceiling of any size fails here rather than silently losing
// bytes.
//
// The small-body control in the same run is what proves the harness measures
// what it claims: without it, a fetch path that returned the served bytes for
// no reason at all would be indistinguishable from a working one.
func TestFetch_LargeBodyLandsComplete(t *testing.T) {
	t.Parallel()

	// Strictly larger than the ceiling that used to truncate here, so this
	// test cannot pass on a body the deleted cap would have allowed through.
	const oversizeLen = 10*1024*1024 + 4096
	if oversizeLen <= 10*1024*1024 {
		t.Fatalf("fixture length %d is not above the removed ceiling; the assertion below would be vacuous", oversizeLen)
	}

	large := make([]byte, oversizeLen)
	for i := range large {
		large[i] = byte('a' + i%26)
	}
	small := []byte("<html><body><p>a small control body</p></body></html>")

	serve := func(t *testing.T, payload []byte) *fetchedPage {
		t.Helper()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write(payload)
		}))
		t.Cleanup(srv.Close)

		c := newFetchClient("", 0)
		page, err := c.fetch(context.Background(), srv.URL)
		if err != nil {
			t.Fatalf("fetch: %v", err)
		}
		return page
	}

	// THE TARGET: a body past the removed ceiling arrives whole.
	page := serve(t, large)
	if len(page.Body) != len(large) {
		t.Errorf("oversize body: got %d bytes, want %d — a ceiling is still truncating the read",
			len(page.Body), len(large))
	}
	gotSum := sha256.Sum256(page.Body)
	wantSum := sha256.Sum256(large)
	if gotSum != wantSum {
		t.Errorf("oversize body: sha256 %x != served %x", gotSum, wantSum)
	}

	// THE CONTROL: the same fetch path round-trips a small body byte-identically.
	ctlPage := serve(t, small)
	if len(ctlPage.Body) != len(small) {
		t.Fatalf("control body: got %d bytes, want %d — the harness itself is broken",
			len(ctlPage.Body), len(small))
	}
	if sha256.Sum256(ctlPage.Body) != sha256.Sum256(small) {
		t.Fatal("control body: sha256 mismatch — the harness itself is broken")
	}
}
