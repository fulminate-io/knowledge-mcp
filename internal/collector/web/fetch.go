// SPDX-License-Identifier: Apache-2.0

package web

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"
)

const (
	defaultUserAgent    = "knowledge-web-collector/0.1 (+github.com/fulminate-io/knowledge-mcp)"
	defaultFetchTimeout = 30 * time.Second
	maxRedirects        = 5
	maxBodyBytes        = 10 * 1024 * 1024 // 10 MiB cap per page
)

// fetchedPage is the result of a single HTTP GET. It is returned even on
// non-2xx responses so callers can record the page node with the status
// code preserved; only transport-level failures (DNS, connection refused,
// context cancellation) return nil + error.
type fetchedPage struct {
	URL       string
	FinalURL  string
	Status    int
	Body      []byte
	Header    http.Header
	FetchedAt time.Time
}

// hostState tracks the last-fetch timestamp for a single host so the
// politeness delay can be enforced per-host without a global mutex.
type hostState struct {
	mu        sync.Mutex
	lastFetch time.Time
}

// fetchClient performs polite, rate-limited HTTP GETs. It follows up to
// maxRedirects redirects, times out at 30s, and sleeps between requests to
// the same host. Safe for concurrent use.
type fetchClient struct {
	httpClient   *http.Client
	userAgent    string
	politenessMs int
	hosts        sync.Map // host(string) -> *hostState
}

// newFetchClient constructs a fetchClient with a 30s-timeout http.Client and
// a CheckRedirect that caps redirects at maxRedirects. politenessMs=0 means
// no inter-request delay (useful for tests).
func newFetchClient(userAgent string, politenessMs int) *fetchClient {
	if userAgent == "" {
		userAgent = defaultUserAgent
	}
	client := &http.Client{
		Timeout: defaultFetchTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxRedirects {
				return http.ErrUseLastResponse
			}
			return nil
		},
	}
	return &fetchClient{
		httpClient:   client,
		userAgent:    userAgent,
		politenessMs: politenessMs,
	}
}

// hostStateFor returns (creating if necessary) the per-host state entry.
func (c *fetchClient) hostStateFor(host string) *hostState {
	if existing, ok := c.hosts.Load(host); ok {
		if hs, ok := existing.(*hostState); ok {
			return hs
		}
	}
	state := &hostState{}
	actual, _ := c.hosts.LoadOrStore(host, state)
	hs, _ := actual.(*hostState)
	return hs
}

// waitForPoliteness sleeps until politenessMs has elapsed since the last
// fetch to host. The per-host mutex is held for the duration of the wait so
// concurrent fetches to the same host serialize cleanly.
func (c *fetchClient) waitForPoliteness(ctx context.Context, state *hostState) error {
	state.mu.Lock()
	defer func() {
		state.lastFetch = time.Now()
		state.mu.Unlock()
	}()
	if c.politenessMs <= 0 || state.lastFetch.IsZero() {
		return nil
	}
	delay := time.Duration(c.politenessMs) * time.Millisecond
	elapsed := time.Since(state.lastFetch)
	if elapsed >= delay {
		return nil
	}
	remaining := delay - elapsed
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(remaining):
		return nil
	}
}

// fetch performs a GET request against rawURL and returns a *fetchedPage
// carrying the URL, final URL after redirects, status code, body, headers,
// and timestamp. Non-2xx responses are returned as *fetchedPage with the
// status code preserved; transport errors return nil + error.
func (c *fetchClient) fetch(ctx context.Context, rawURL string) (*fetchedPage, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("parse url %q: %w", rawURL, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("unsupported scheme %q", parsed.Scheme)
	}
	if parsed.Host == "" {
		return nil, fmt.Errorf("missing host in url %q", rawURL)
	}

	state := c.hostStateFor(parsed.Host)
	if err := c.waitForPoliteness(ctx, state); err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		return nil, fmt.Errorf("http get %q: %w", rawURL, err)
	}
	defer resp.Body.Close()

	body, err := readLimited(resp.Body, maxBodyBytes)
	if err != nil {
		return nil, fmt.Errorf("read body %q: %w", rawURL, err)
	}

	finalURL := rawURL
	if resp.Request != nil && resp.Request.URL != nil {
		finalURL = resp.Request.URL.String()
	}

	return &fetchedPage{
		URL:       rawURL,
		FinalURL:  finalURL,
		Status:    resp.StatusCode,
		Body:      body,
		Header:    resp.Header.Clone(),
		FetchedAt: time.Now().UTC(),
	}, nil
}

// readLimited reads up to max bytes from r, returning an error only if the
// underlying reader errors (EOF is not an error). Truncation at the cap is
// silent by design — we want a page node even if the body is huge.
func readLimited(r io.Reader, max int64) ([]byte, error) {
	lr := io.LimitReader(r, max)
	body, err := io.ReadAll(lr)
	if err != nil {
		return nil, err
	}
	return body, nil
}
