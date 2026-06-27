// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
)

// Transport is the client-side half of the push-only sync protocol. It wraps
// one server endpoint:
//
//	POST /v1/sync/push/<graphType>/<name>     — upload a serialized graph
//
// plus a single HTTP helper that handles Bearer auth and 401-refresh-retry.
//
// The zero value is not usable; construct via [NewSyncTransport]. Transport
// is safe for concurrent use — the HTTP client is inherently thread-safe
// and the [TokenSource] implementations we ship serialize their own state
// via internal mutexes.
//
// Transport never reaches into tools/ — it operates on opaque graph bytes.
// The client push intercept produces those bytes via the EngineService.
// ExportGraph read ([store.SerializeGraph]) and uploads them here.
//
// Import contract: this package MUST NOT import domains/store (cycle).
// The graphType parameter on PushGraph is a plain string; validation
// happens client-side in the push intercept.
type Transport struct {
	endpoint   string
	source     TokenSource
	httpClient *http.Client
	logger     *slog.Logger
}

// TransportOption configures a [Transport] at construction time. See the
// With* functions in this file.
type TransportOption func(*Transport)

// defaultSyncClientTimeout is the per-request timeout applied to the
// Transport's HTTP client. Graphs up to ~100 MB are expected to complete
// inside this window on a reasonable connection.
const defaultSyncClientTimeout = 5 * time.Minute

// NewSyncTransport constructs a [Transport] targeting the given API
// endpoint. endpoint must be the scheme+host+port without a trailing slash
// and without a /v1 suffix — the Transport appends /v1/sync/... to each
// route. source supplies the bearer credential (OAuth JWT with `sync`
// scope or the legacy knowledge_token).
func NewSyncTransport(endpoint string, source TokenSource, opts ...TransportOption) *Transport {
	t := &Transport{
		endpoint: endpoint,
		source:   source,
		httpClient: &http.Client{
			Timeout: defaultSyncClientTimeout,
		},
		logger: slog.Default(),
	}
	for _, opt := range opts {
		opt(t)
	}
	return t
}

// PushGraph uploads a serialized graph to the server. body is the raw
// bytes produced by store.SerializeGraph; the server merges them into its
// copy of (graphType, name) using UpdatedAt-wins semantics. graphType is
// a plain string ("knowledge", "code", "cloud", ...) — validation is the
// caller's responsibility (auth does not import store, so it cannot
// reference store.GraphType values).
//
// The body is sent in a single POST; no multipart, no presigned URLs.
// Under the full-graph shape "push" and "server-side merge" are atomic
// from the client's view: a successful 2xx response means the server has
// already applied the merge.
func (t *Transport) PushGraph(
	ctx context.Context,
	graphType string,
	name string,
	body []byte,
) error {
	if graphType == "" || name == "" {
		return fmt.Errorf("auth: push: graphType and name required")
	}
	path := "push/" + graphType + "/" + name
	resp, err := t.sendWithAuthBytes(ctx, http.MethodPost, path, body)
	if err != nil {
		return fmt.Errorf("auth: push %s/%s: %w", graphType, name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return readHTTPError(resp, path)
	}
	// Drain so the connection can be reused.
	_, _ = io.Copy(io.Discard, resp.Body)
	t.logger.Debug("sync: push succeeded",
		"graphType", graphType, "name", name, "bytes", len(body))
	return nil
}

// SyncControlJSON issues a Bearer-authenticated POST to a /v1/sync/<path>
// control-plane endpoint with the given JSON request body and returns the raw
// JSON response body on a 2xx. It is the small-control-request counterpart of
// PushGraph: the presigned-direct-to-GCS sync flow (presign / confirm / pull)
// crosses Cloudflare only with these small JSON control requests, while the bulk
// (encrypted) graph bytes go straight to/from GCS off-band.
//
// It reuses sendWithAuthBytes (so 401-refresh-retry is identical). The agent
// control-plane handlers read the JSON body irrespective of the request
// Content-Type (issueBytes labels the body octet-stream) and respond with JSON;
// a non-2xx surfaces as a *SyncHTTPError so callers get the same auth-failure
// classification PushGraph provides.
func (t *Transport) SyncControlJSON(ctx context.Context, path string, reqBody []byte) ([]byte, error) {
	resp, err := t.sendWithAuthBytes(ctx, http.MethodPost, path, reqBody)
	if err != nil {
		return nil, fmt.Errorf("auth: sync control %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, readHTTPError(resp, path)
	}
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("auth: read sync control %s response: %w", path, err)
	}
	return out, nil
}

// sendWithAuthBytes issues a single request (method + path + body) with
// the current Bearer credential. On HTTP 401 from a refreshing token
// source it force-refreshes and retries once. Used by PushGraph; kept as a
// standalone helper so the token-refresh logic stays isolated from the route.
//
// Returns the raw *http.Response; the caller owns the body lifecycle.
// The POST-with-body payload bytes are sent as application/octet-stream.
func (t *Transport) sendWithAuthBytes(
	ctx context.Context,
	method string,
	path string,
	body []byte,
) (*http.Response, error) {
	token, _, err := t.source.Token(ctx)
	if err != nil {
		return nil, fmt.Errorf("auth: acquire token for %s %s: %w",
			method, path, err)
	}

	resp, err := t.issueBytes(ctx, method, path, body, token)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusUnauthorized {
		return resp, nil
	}

	// 401 — force-refresh and retry if the source supports it.
	refresher, ok := t.source.(RefreshingTokenSource)
	if !ok {
		return resp, nil // caller sees the 401 and surfaces it
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()

	t.logger.Debug("sync: 401 from server — forcing token refresh and retrying once",
		"path", path)
	newToken, _, refreshErr := refresher.ForceRefresh(ctx)
	if refreshErr != nil {
		return nil, fmt.Errorf("auth: force-refresh after 401 on %s %s: %w",
			method, path, refreshErr)
	}
	return t.issueBytes(ctx, method, path, body, newToken)
}

// issueBytes constructs and sends one request with the given bearer
// token. It does NOT read or close the response — callers own the
// response lifecycle (so sendWithAuthBytes can inspect .StatusCode and
// decide whether to retry before draining the body).
func (t *Transport) issueBytes(
	ctx context.Context,
	method string,
	path string,
	body []byte,
	token string,
) (*http.Response, error) {
	url := t.endpoint + "/v1/sync/" + path
	var bodyReader io.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("auth: build %s %s: %w", method, path, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/octet-stream")
	}
	req.Header.Set("Accept", "application/octet-stream")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := t.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("auth: send %s %s: %w", method, path, err)
	}
	return resp, nil
}

// maxErrorBodyBytes caps how much of a non-2xx response body we hold in
// memory.
const maxErrorBodyBytes = 8 * 1024

// readHTTPError reads up to maxErrorBodyBytes of the response body and
// returns a *SyncHTTPError describing the failure.
func readHTTPError(resp *http.Response, path string) error {
	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
	body := ""
	if readErr == nil {
		body = string(raw)
	}
	return &SyncHTTPError{
		Path:       path,
		StatusCode: resp.StatusCode,
		Body:       body,
	}
}

// SyncHTTPError is returned when a /v1/sync/* endpoint returns a non-2xx
// status code. The Body field holds up to [maxErrorBodyBytes] of the raw
// server response, truncated to avoid runaway log growth. Callers can
// errors.As() on this type to inspect status codes — common checks are
// StatusCode == 401 (auth rejected even after refresh) and 403 (scope
// missing, cross-account denial).
type SyncHTTPError struct {
	Path       string
	StatusCode int
	Body       string
}

// Error implements the error interface.
func (e *SyncHTTPError) Error() string {
	return fmt.Sprintf("auth: /v1/sync/%s: HTTP %d: %s",
		e.Path, e.StatusCode, e.Body)
}
