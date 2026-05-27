// SPDX-License-Identifier: Apache-2.0

package web

import (
	"context"
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
	var hits int32
	mux := http.NewServeMux()
	// /loop redirects to /loop — unbounded unless capped.
	mux.HandleFunc("/loop", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
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
	total := atomic.LoadInt32(&hits)
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
