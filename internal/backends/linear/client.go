// SPDX-License-Identifier: Apache-2.0

// Package linear implements the backends.Backend interface against
// api.linear.app. Hand-rolled GraphQL queries; no codegen.
//
// # Auth
//
// The Linear key is a "Personal API Key" (lin_api_xxx prefix). Per Linear's
// docs, personal API keys are passed in the Authorization header VERBATIM
// with no "Bearer " prefix. The Bearer prefix is reserved for OAuth access
// tokens; this adapter does not implement OAuth.
//
// The key is resolved via config.LinearAPIKey(): the [credentials].
// linear_api_key field in ~/.knowledge/config if set, otherwise the
// LINEAR_API_KEY env var. Storing it in the config file makes the daemon
// launch-method-agnostic.
//
// # Activation
//
// Enabled() returns true iff config.LinearAPIKey() != "". The closed-switch
// provider in internal/backends/provider/provider.go consults Enabled() before
// instantiating.
package linear

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/backends"
	"github.com/fulminate-io/knowledge-mcp/internal/config"
)

// DefaultEndpoint is api.linear.app's GraphQL endpoint.
const DefaultEndpoint = "https://api.linear.app"

// httpClient is shared at package level so every Client built without an
// explicit *http.Client reuses one connection pool rather than one per Client.
// The 30 s timeout was adopted from a fulminate REST client that has since been
// removed from this repo, so this declaration is now the convention's only home
// here — it is not kept in step with anything else in the tree.
var httpClient = &http.Client{Timeout: 30 * time.Second}

// Enabled reports whether a Linear key is configured — either via
// [credentials].linear_api_key in ~/.knowledge/config or the LINEAR_API_KEY
// env var (see config.LinearAPIKey). The closed-switch provider consults
// this before constructing a Backend.
func Enabled() bool { return config.LinearAPIKey() != "" }

// Client is the HTTP transport. It does NOT implement backends.Backend on its
// own — Phase 3 wraps it in a Backend struct. Splitting transport from the
// adapter lets us unit-test request shape + response parsing without going
// through the Backend method signatures.
type Client struct {
	APIKey   string
	Endpoint string // base; "/graphql" appended in do()
	HTTP     *http.Client
}

// NewClient returns a Client preconfigured with the resolved key (config
// [credentials].linear_api_key, then LINEAR_API_KEY env var — see
// config.LinearAPIKey), the default endpoint, and the package-level HTTP
// client. Tests can construct a Client literal directly to inject
// httptest.Server.URL + srv.Client(); the New helper is the production path.
func NewClient() *Client {
	return &Client{
		APIKey:   config.LinearAPIKey(),
		Endpoint: DefaultEndpoint,
		HTTP:     httpClient,
	}
}

// gqlEnvelope is the wire request body shape: every Linear GraphQL call is
// `POST { query: "...", variables: {...} }`.
type gqlEnvelope struct {
	Query     string `json:"query"`
	Variables any    `json:"variables,omitempty"`
}

// gqlResponse is the wire response envelope. `Data` is decoded into the
// caller-supplied `out` via a second json.Unmarshal pass; `Errors` is
// summarized into a wrapped Go error.
type gqlResponse struct {
	Data   json.RawMessage `json:"data"`
	Errors []graphQLError  `json:"errors,omitempty"`
}

// classifyHTTPStatus maps non-2xx HTTP responses to the typed
// *backends.Error envelope (auth/rate_limited/http_5xx/validation).
// Returns nil for successful 2xx so the caller can proceed to body decode.
func classifyHTTPStatus(resp *http.Response, url string) error {
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return &backends.Error{
			Transient: false,
			Reason:    backends.ReasonAuth,
			Cause:     ErrUnauthorized,
		}
	case resp.StatusCode == http.StatusTooManyRequests:
		b, _ := io.ReadAll(resp.Body)
		return &backends.Error{
			Transient: true,
			Reason:    backends.ReasonRateLimited,
			Cause:     fmt.Errorf("linear: 429 too many requests: %s", string(b)),
		}
	case resp.StatusCode >= 500 && resp.StatusCode < 600:
		b, _ := io.ReadAll(resp.Body)
		return &backends.Error{
			Transient: true,
			Reason:    backends.ReasonHTTP5xx,
			Cause:     fmt.Errorf("linear: POST %s returned %d: %s", url, resp.StatusCode, string(b)),
		}
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		// 4xx (other than 401/403/429) — terminal validation.
		b, _ := io.ReadAll(resp.Body)
		return &backends.Error{
			Transient: false,
			Reason:    backends.ReasonValidation,
			Cause:     fmt.Errorf("linear: POST %s returned %d: %s", url, resp.StatusCode, string(b)),
		}
	}
	return nil
}

// do POSTs a GraphQL query to <Endpoint>/graphql with the configured API
// key as the raw Authorization header. On HTTP success it decodes
// response.data into `out` (which must be a pointer). On HTTP failure
// (non-2xx) or GraphQL `errors[]` it returns a wrapped error.
//
// The caller passes the query string and variables as a Go-shape map (or
// typed struct); json.Marshal turns the variables into the GraphQL JSON
// literal Linear expects.
func (c *Client) do(ctx context.Context, query string, variables any, out any) error {
	body, err := json.Marshal(gqlEnvelope{Query: query, Variables: variables})
	if err != nil {
		return fmt.Errorf("linear: marshal request: %w", err)
	}

	url := c.Endpoint + "/graphql"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("linear: build request: %w", err)
	}
	// RAW key, no "Bearer " prefix — Linear personal API key convention.
	req.Header.Set("Authorization", c.APIKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		// Classify transport errors: ctx cancel/deadline → timeout;
		// everything else → network. Both transient.
		reason := backends.ReasonNetwork
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			reason = backends.ReasonTimeout
		}
		return &backends.Error{
			Transient: true,
			Reason:    reason,
			Cause:     fmt.Errorf("linear: POST %s failed: %w", url, err),
		}
	}
	defer resp.Body.Close()

	if err := classifyHTTPStatus(resp, url); err != nil {
		return err
	}

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		// Body-read mid-response — treat as transient network error.
		return &backends.Error{
			Transient: true,
			Reason:    backends.ReasonNetwork,
			Cause:     fmt.Errorf("linear: read response body: %w", err),
		}
	}
	var env gqlResponse
	if err := json.Unmarshal(raw, &env); err != nil {
		// Malformed JSON from server — terminal (no useful retry).
		return &backends.Error{
			Transient: false,
			Reason:    backends.ReasonGraphQL,
			Cause:     fmt.Errorf("linear: decode response envelope: %w", err),
		}
	}
	if len(env.Errors) > 0 {
		return &backends.Error{
			Transient: false,
			Reason:    backends.ReasonGraphQL,
			Cause:     fmt.Errorf("linear: GraphQL error: %s", formatGraphQLError(env.Errors[0])),
		}
	}
	if out != nil {
		if err := json.Unmarshal(env.Data, out); err != nil {
			return fmt.Errorf("linear: decode data: %w", err)
		}
	}
	return nil
}
