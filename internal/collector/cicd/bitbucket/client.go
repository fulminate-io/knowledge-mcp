// SPDX-License-Identifier: Apache-2.0

package bitbucket

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	defaultBaseURL = "https://api.bitbucket.org/2.0"
	defaultTimeout = 30 * time.Second
	maxPagelen     = 100 // Bitbucket API max items per page
	maxRetries     = 3   // max retries on 429
)

// Client is a thin HTTP client for the Bitbucket REST API v2.0.
// Auth uses HTTP Basic with username + app password.
type Client struct {
	baseURL     string
	username    string
	appPassword string
	httpClient  *http.Client
}

// NewClient creates a Bitbucket API client with a 30s timeout.
func NewClient(username, appPassword string) *Client {
	return &Client{
		baseURL:     defaultBaseURL,
		username:    username,
		appPassword: appPassword,
		httpClient: &http.Client{
			Timeout: defaultTimeout,
		},
	}
}

// APIError represents a non-2xx HTTP response from the Bitbucket API.
type APIError struct {
	StatusCode int
	Body       string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("bitbucket API %d: %s", e.StatusCode, e.Body)
}

// GetPaginated iterates through all pages of a paginated Bitbucket endpoint.
// Bitbucket responses have shape: {"values": [...], "next": "url"}.
// handler is called once per page with the raw "values" JSON array.
func (c *Client) GetPaginated(
	ctx context.Context,
	path string,
	handler func(raw json.RawMessage) error,
) error {
	url := c.resolveURL(path)
	if !strings.Contains(url, "pagelen=") {
		sep := "?"
		if strings.Contains(url, "?") {
			sep = "&"
		}
		url += sep + "pagelen=" + strconv.Itoa(maxPagelen)
	}

	for url != "" {
		if err := ctx.Err(); err != nil {
			return err
		}

		page, nextURL, err := c.fetchPage(ctx, url)
		if err != nil {
			return err
		}

		if page != nil {
			if err := handler(page); err != nil {
				return fmt.Errorf("page handler: %w", err)
			}
		}

		url = nextURL
	}
	return nil
}

// paginatedResponse captures the Bitbucket pagination envelope.
type paginatedResponse struct {
	Values json.RawMessage `json:"values"`
	Next   string          `json:"next"`
}

// fetchPage fetches a single page and returns the values array and next URL.
func (c *Client) fetchPage(
	ctx context.Context, url string,
) (json.RawMessage, string, error) {
	resp, err := c.doWithRetry(ctx, url)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", readAPIError(resp)
	}

	var page paginatedResponse
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		return nil, "", fmt.Errorf("decode page: %w", err)
	}
	return page.Values, page.Next, nil
}

// resolveURL returns an absolute URL. If path starts with "http", it is
// returned as-is; otherwise it is joined to the base URL.
func (c *Client) resolveURL(path string) string {
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return path
	}
	return strings.TrimRight(c.baseURL, "/") + "/" + strings.TrimLeft(path, "/")
}

// doWithRetry executes a GET request with retry on 429 Too Many Requests.
// URL is built from baseURL + caller-provided path — not arbitrary user input.
func (c *Client) doWithRetry(ctx context.Context, url string) (*http.Response, error) {
	for attempt := range maxRetries {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil) //nolint:gosec // URL from trusted API path
		if err != nil {
			return nil, fmt.Errorf("build request: %w", err)
		}
		req.SetBasicAuth(c.username, c.appPassword)

		resp, err := c.httpClient.Do(req) //nolint:gosec // URL from trusted API path
		if err != nil {
			return nil, fmt.Errorf("http GET %s: %w", url, err)
		}

		if resp.StatusCode != http.StatusTooManyRequests {
			return resp, nil
		}

		resp.Body.Close()
		wait := parseRetryAfter(resp.Header.Get("Retry-After"))
		slog.Warn("bitbucket: rate limited", //nolint:gosec // url is not user-controlled taint
			"attempt", attempt+1, "wait", wait, "url", url)

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(wait):
		}
	}

	// Final attempt — return whatever we get.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil) //nolint:gosec // URL from trusted API path
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.SetBasicAuth(c.username, c.appPassword)
	return c.httpClient.Do(req) //nolint:gosec // URL from trusted API path
}

// parseRetryAfter parses the Retry-After header value (seconds).
// Returns a 2s default if the header is missing or unparseable.
func parseRetryAfter(header string) time.Duration {
	if header == "" {
		return 2 * time.Second
	}
	seconds, err := strconv.Atoi(header)
	if err != nil {
		return 2 * time.Second
	}
	if seconds <= 0 {
		return 1 * time.Second
	}
	return time.Duration(seconds) * time.Second
}

// GetRaw performs a GET request and returns the raw response body.
// Useful for non-JSON endpoints (e.g. fetching raw file content).
func (c *Client) GetRaw(ctx context.Context, path string) ([]byte, error) {
	url := c.resolveURL(path)
	resp, err := c.doWithRetry(ctx, url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, readAPIError(resp)
	}

	return io.ReadAll(resp.Body)
}

// readAPIError reads the response body and wraps it in an APIError.
func readAPIError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return &APIError{StatusCode: resp.StatusCode, Body: string(body)}
}
