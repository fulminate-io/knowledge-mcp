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

	"github.com/fulminate-io/knowledge-mcp/internal/clientver"
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
// Import contract: this package MUST NOT import a store package.
// The graphType parameter on PushGraph is a plain string; validation
// happens client-side in the push intercept.
type Transport struct {
	endpoint   string
	source     TokenSource
	httpClient *http.Client
	logger     *slog.Logger
	// sel overrides the process-wide account selection. Set by tests via
	// [WithAccountSelection]; nil in production, where [Transport.selection]
	// falls back to [SelectedAccount].
	sel *AccountSelection
	// proveAnswer enables the one-shot prove-on-refusal recovery and supplies
	// the possession-proof answer function. nil (the default) disables it
	// entirely: the daemon owns proving through its background loop and must
	// not gain a second prover on the request path. Set via
	// [WithProveOnRefusal]; see version_prove_on_refusal.go.
	proveAnswer func(nonce []byte, offset, length int64) (string, error)
	// proveState is the single-attempt guard, at most one exchange per
	// Transport — and one Transport per CLI invocation.
	proveState proveOnRefusalState
}

// TransportOption configures a [Transport] at construction time. See the
// With* functions in this file.
type TransportOption func(*Transport)

// WithAccountSelection overrides the account selection this Transport consults
// when stamping the Knowledge-Account-Id header. It exists for TESTS.
//
// Production construction sites deliberately leave it unset and inherit the
// process singleton: an option a construction site can forget to pass would
// reproduce exactly the silently-split-across-accounts failure this feature
// exists to prevent, whereas a default every Transport reads cannot be
// forgotten.
func WithAccountSelection(sel *AccountSelection) TransportOption {
	return func(t *Transport) { t.sel = sel }
}

// SetHTTPClientForTest replaces the HTTP client this Transport issues through.
// It exists for TESTS, in the same spirit as [WithAccountSelection].
//
// It is a post-construction setter rather than an option because the case it
// serves is a test that must drive a transport built by the REAL constructor —
// which takes no options from its caller and pins its endpoint to a build-tag
// constant with no runtime override. Redirecting the client is how such a test
// reaches a stub server WITHOUT making that endpoint overridable, which would
// weaken a deliberate production property to make a fixture convenient.
func (t *Transport) SetHTTPClientForTest(c *http.Client) { t.httpClient = c }

// selection returns the test-injected selection when one is set, otherwise the
// process-wide one.
func (t *Transport) selection() *AccountSelection {
	if t.sel != nil {
		return t.sel
	}
	return SelectedAccount()
}

// defaultSyncClientTimeout is the per-request timeout applied to the
// Transport's HTTP client. Graphs up to ~100 MB are expected to complete
// inside this window on a reasonable connection.
const defaultSyncClientTimeout = 5 * time.Minute

// syncPathPrefix is the route prefix of the one control channel the Transport
// speaks: the server/agent register the graph-sync endpoints under /v1/sync/.
// sendWithAuthBytes/issueBytes still take it as a parameter because they are the
// single Bearer + 401-refresh core and the prefix belongs to the route, not to
// them.
const syncPathPrefix = "/v1/sync/"

// octetStreamAccept is the Accept header the graph-bytes and control-JSON
// routes have always advertised. Named so the accept parameter added for the
// JSON GET routes keeps those routes' wire bytes identical.
const octetStreamAccept = "application/octet-stream"

// NewSyncTransport constructs a [Transport] targeting the given API
// endpoint. endpoint must be the scheme+host+port without a trailing slash
// and without a /v1 suffix — the Transport appends the route prefix
// (/v1/sync/...) to each route. source supplies the bearer credential (OAuth JWT
// with `sync` scope or the legacy knowledge_token).
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
	resp, err := t.sendWithAuthBytes(ctx, http.MethodPost, syncPathPrefix, path, octetStreamAccept, body, false)
	if err != nil {
		return fmt.Errorf("auth: push %s/%s: %w", graphType, name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return t.readHTTPError(ctx, resp, syncPathPrefix+path)
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
	return t.controlJSON(ctx, path, reqBody)
}

// controlJSON is the body behind SyncControlJSON, its ONE caller: POST reqBody
// under the sync route prefix, surface a non-2xx as a *SyncHTTPError, and return
// the raw 2xx JSON body verbatim. The prefix and the "sync" error label are
// inlined rather than taken as parameters — with one channel left there is
// nothing for a caller to vary, and a parameter preserved for a caller that no
// longer varies it is dead surface.
func (t *Transport) controlJSON(ctx context.Context, path string, reqBody []byte) ([]byte, error) {
	resp, err := t.sendWithAuthBytes(ctx, http.MethodPost, syncPathPrefix, path, octetStreamAccept, reqBody, false)
	if err != nil {
		return nil, fmt.Errorf("auth: sync control %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, t.readHTTPError(ctx, resp, syncPathPrefix+path)
	}
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("auth: read sync control %s response: %w", path, err)
	}
	return out, nil
}

// sendWithAuthBytes issues a single request (method + prefix + path + body) with
// the current Bearer credential. On HTTP 401 from a refreshing token
// source it force-refreshes and retries once. Used by PushGraph and by the sync
// control channel (SyncControlJSON); kept as a standalone helper so the
// token-refresh logic stays isolated from the route the prefix parameter names.
//
// Returns the raw *http.Response; the caller owns the body lifecycle.
// The POST-with-body payload bytes are sent as application/octet-stream;
// accept is the Accept header the request advertises (the bytes routes pass
// [octetStreamAccept]; the JSON GET routes pass application/json).
// bypassAccountRefusal is threaded to issueBytes as a PER-CALL parameter, never
// as a field or flag on the Transport: the daemon shares one Transport across
// the segment control plane, the sync push/pull intercept and the transcript
// upload loop, so a set-then-reset flag would be a data race in which a
// concurrent push could read the bypass and skip the known-invalid refusal.
func (t *Transport) sendWithAuthBytes(
	ctx context.Context,
	method string,
	prefix string,
	path string,
	accept string,
	body []byte,
	bypassAccountRefusal bool,
) (*http.Response, error) {
	token, _, err := t.source.Token(ctx)
	if err != nil {
		return nil, fmt.Errorf("auth: acquire token for %s %s: %w",
			method, path, err)
	}

	resp, err := t.issueBytes(ctx, method, prefix, path, accept, body, token, bypassAccountRefusal)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusUnauthorized {
		// A version refusal this client can repair by proving: prove once,
		// then re-issue the IDENTICAL request from the same body bytes. Every
		// other refusal — and every later one on this Transport — falls
		// through with its body restored and its remedy intact.
		if t.maybeProveAndRetry(ctx, resp) {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			return t.issueBytes(ctx, method, prefix, path, accept, body, token, bypassAccountRefusal)
		}
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
	return t.issueBytes(ctx, method, prefix, path, accept, body, newToken, bypassAccountRefusal)
}

// issueBytes constructs and sends one request with the given bearer
// token. It does NOT read or close the response — callers own the
// response lifecycle (so sendWithAuthBytes can inspect .StatusCode and
// decide whether to retry before draining the body).
//
// This is the single stamping point for the /v1/sync/* surface: PushGraph and
// SyncControlJSON both reach it, so the Knowledge-Account-Id header cannot be
// attached to one route and missed on another.
func (t *Transport) issueBytes(
	ctx context.Context,
	method string,
	prefix string,
	path string,
	accept string,
	body []byte,
	token string,
	bypassAccountRefusal bool,
) (*http.Response, error) {
	url := t.endpoint + prefix + path
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
	req.Header.Set("Accept", accept)
	req.Header.Set("Authorization", "Bearer "+token)
	// The client's build identity rides every cloud-bound request. This is the
	// single stamping point for the whole /v1/sync/* surface (see the doc
	// comment above), so PushGraph, the control channel and the version
	// challenge that rides it all carry the headers without separate plumbing.
	clientver.Stamp(req.Header)

	// Stamp or refuse. An empty id sets NO header at all (not an empty one),
	// which preserves the unset semantics exactly: no config entry -> no
	// header -> the gateway resolves the caller's primary account as before.
	// A selection the gateway has already rejected returns here, BEFORE the
	// round trip is issued.
	//
	// bypassAccountRefusal stamps a valid selection but never refuses, for the
	// recovery routes: a user whose selection has been rejected must still be
	// able to run the command that lists the accounts they may pick.
	var acct string
	if bypassAccountRefusal {
		acct = t.selection().ID(ctx)
	} else {
		var acctErr error
		acct, acctErr = t.selection().IDForRequest(ctx)
		if acctErr != nil {
			return nil, fmt.Errorf("auth: %s %s: %w", method, path, acctErr)
		}
	}
	if acct != "" {
		req.Header.Set(AccountHeaderName, acct)
	}

	resp, err := t.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("auth: send %s %s: %w", method, path, err)
	}
	return resp, nil
}

// MaxErrorBodyBytes caps how much of a non-2xx response body we hold in
// memory. Exported so the Connect chain's round-tripper classifies rejection
// bodies under the SAME cap rather than inventing a second one.
const MaxErrorBodyBytes = 8 * 1024

// readHTTPError reads up to MaxErrorBodyBytes of the response body, classifies
// it as a possible account rejection, and returns a *SyncHTTPError describing
// the failure.
//
// When the rejection settles the selection's validity, the selection is marked
// invalid here so the NEXT cloud-bound call fails fast locally instead of
// round-tripping to a guaranteed rejection. There is deliberately NO
// retry-without-the-header path: dropping the header would route the user's
// writes into a DIFFERENT account than the one they selected, which is the
// data-splitting failure this feature exists to prevent.
func (t *Transport) readHTTPError(ctx context.Context, resp *http.Response, path string) error {
	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, MaxErrorBodyBytes))
	body := ""
	if readErr == nil {
		body = string(raw)
	}
	// A version refusal is answered FIRST and returned as its own error: it is
	// the gateway refusing this client outright, and reporting it as a generic
	// non-2xx would lose the remedy the user needs. It is never swallowed and
	// never downgraded into a warning.
	if refusal, ok := LatchVersionRefusal(RefusalObservation{
		Status:    resp.StatusCode,
		Header:    resp.Header,
		Body:      raw,
		ReadErr:   readErr,
		Transport: "sync",
		Path:      path,
	}); ok {
		return refusal
	}
	reason, latch := ClassifyAccountRejection(resp.StatusCode, raw)
	if latch {
		if id := t.selection().ID(ctx); id != "" {
			t.selection().MarkInvalid(id, reason)
		}
	}
	return &SyncHTTPError{
		Path:          path,
		StatusCode:    resp.StatusCode,
		Body:          body,
		AccountReason: reason,
	}
}

// SyncHTTPError is returned when a /v1/sync/* endpoint returns a non-2xx status
// code. Path is the full route (prefix included, e.g. "/v1/sync/presign"). The
// Body field holds up to [MaxErrorBodyBytes] of the
// raw server response, truncated to avoid runaway log growth. Callers can
// errors.As() on this type to inspect status codes — common checks are
// StatusCode == 401 (auth rejected even after refresh) and 403 (scope
// missing, cross-account denial).
//
// AccountReason carries the operator-facing remedy when the response was
// classified as an account rejection ([ClassifyAccountRejection]); it is empty
// for every other failure.
type SyncHTTPError struct {
	Path          string
	StatusCode    int
	Body          string
	AccountReason string
}

// Error implements the error interface.
func (e *SyncHTTPError) Error() string {
	if e.AccountReason != "" {
		return fmt.Sprintf("auth: %s: HTTP %d: %s — %s",
			e.Path, e.StatusCode, e.Body, e.AccountReason)
	}
	return fmt.Sprintf("auth: %s: HTTP %d: %s",
		e.Path, e.StatusCode, e.Body)
}
