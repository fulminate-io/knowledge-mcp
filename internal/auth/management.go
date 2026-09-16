// SPDX-License-Identifier: Apache-2.0
package auth

import (
	"context"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
)

var managementID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,199}$`)

// ManagementHTTPError exposes status, never an upstream body or credential.
type ManagementHTTPError struct{ Status int }

func (e *ManagementHTTPError) Error() string { return "management request refused" }

// Environment performs one existing environment operation. The captured account
// binds both path and header through token refresh; configuration changes affect
// subsequent operations, never the retry of an in-flight operation.
func (t *Transport) Environment(ctx context.Context, account, operation, id string) ([]byte, error) {
	if !managementID.MatchString(account) {
		return nil, errors.New("invalid management account")
	}
	method := http.MethodGet
	suffix := ""
	switch operation {
	case "list":
		if id != "" {
			return nil, errors.New("invalid management environment")
		}
	case "get", "start", "stop":
		if !managementID.MatchString(id) {
			return nil, errors.New("invalid management environment")
		}
		suffix = "/" + id
		if operation != "get" {
			method = http.MethodPost
			suffix += "/" + operation
		}
	default:
		return nil, errors.New("invalid management operation")
	}
	return t.managementRequest(ctx, account, method, "/v1/accounts/", account+"/dev-vm"+suffix, nil)
}

// EnvironmentWrite uses only the existing account-scoped management routes.
func (t *Transport) EnvironmentWrite(ctx context.Context, account, operation, id string, body []byte) ([]byte, error) {
	return t.managementWrite(ctx, account, "dev-vm", operation, id, body)
}

// DashboardWrite edits the saved document; it never executes its endpoints.
func (t *Transport) DashboardWrite(ctx context.Context, account, operation, id string, body []byte) ([]byte, error) {
	return t.managementWrite(ctx, account, "ui-documents", operation, id, body)
}

func (t *Transport) managementWrite(ctx context.Context, account, resource, operation, id string, body []byte) ([]byte, error) {
	if !managementID.MatchString(account) {
		return nil, errors.New("invalid management account")
	}
	suffix := account + "/" + resource
	method := ""
	switch operation {
	case "create":
		if id != "" {
			return nil, errors.New("invalid management identity")
		}
		method = http.MethodPost
	case "update", "delete", "publish":
		if !managementID.MatchString(id) {
			return nil, errors.New("invalid management identity")
		}
		suffix += "/" + id
		switch operation {
		case "update":
			method = http.MethodPut
		case "delete":
			method = http.MethodDelete
		case "publish":
			if resource != "ui-documents" || len(body) != 0 {
				return nil, errors.New("invalid management operation")
			}
			method = http.MethodPost
			suffix += "/publish"
		}
	default:
		return nil, errors.New("invalid management operation")
	}
	return t.managementRequest(ctx, account, method, "/v1/accounts/", suffix, body)
}

// Dashboard reads the existing account-scoped document API.
func (t *Transport) Dashboard(ctx context.Context, account, id string) ([]byte, error) {
	if !managementID.MatchString(account) || (id != "" && !managementID.MatchString(id)) {
		return nil, errors.New("invalid dashboard identity")
	}
	suffix := account + "/ui-documents"
	if id != "" {
		suffix += "/" + id
	}
	return t.managementRequest(ctx, account, http.MethodGet, "/v1/accounts/", suffix, nil)
}

// DashboardConnect mints the existing native connection capability for the captured account.
func (t *Transport) DashboardConnect(ctx context.Context, account string, body []byte) ([]byte, error) {
	if !managementID.MatchString(account) {
		return nil, errors.New("invalid dashboard account")
	}
	return t.managementRequest(ctx, account, http.MethodPost, "/v1/dev-vm/", "connect", body)
}

// PlatformCall is one proxied platform request. It is a struct rather than a
// parameter list because the path and the query are both strings whose roles
// differ entirely: the path is validated here and the query is deliberately
// NOT, so a caller that swapped them would send a filter value as a path
// segment. Naming them at every call site is what makes that swap unwritable.
type PlatformCall struct {
	// Account names the account the ROUTE asserts, and is empty for a
	// caller-scoped route. It is not the account header's source; see below.
	Account string
	Method  string
	// Path is a resolved path with no query and no fragment, starting with a
	// slash.
	Path string
	// Query is an already-encoded query string without its leading question
	// mark, or empty. The caller owns its escaping: this method joins it to
	// the path AFTER the path guard has run, which is the whole reason it is a
	// separate value (see the guard's comment below).
	Query string
	Body  []byte
}

// PlatformResponse is what one proxied platform request answered. ContentType
// is the response's own Content-Type header verbatim, or empty when the
// platform sent none; the Desktop proxy selects its response arm from it, so
// discarding it here would leave that arm no source.
type PlatformResponse struct {
	Status      int
	ContentType string
	Body        []byte
}

// PlatformResponseLimit is the largest platform response body PlatformRequest
// will read. It is exported because the Desktop proxy declares its own, smaller
// body cap and has to be able to assert that this limit is the outer one: if
// the two ever crossed, the proxy's named too-large refusal would become
// unreachable and an oversize answer would surface as a transport failure
// instead.
const PlatformResponseLimit = 16 << 20

// ErrPlatformResponseTooLarge is returned when a platform response exceeds
// [PlatformResponseLimit]. It is a sentinel rather than an inline error because
// the Desktop proxy must report an oversize answer as its own named outcome and
// never as an unreachable platform; string-matching a message to tell those
// apart is the failure this sentinel prevents.
var ErrPlatformResponseTooLarge = errors.New("platform response too large")

// PlatformRequest performs one Desktop-proxied platform request and returns the
// response status, its content type and its body verbatim.
//
// A non-2xx is NOT an error here, which is the one way this differs from every
// other method in this file. The Desktop renderer hosts the website's own
// components, and those components decide what a refusal means: a 401 has to
// reach them as a 401 so the shell renders its signed-out state, and collapsing
// it into an error would leave the renderer with a failed call and no way to
// tell a rejected credential from an unreachable service. Only a token or
// transport failure — nothing the platform said — returns err.
//
// The bearer is attached inside issueBytes from this process's token source and
// crosses no other boundary: the caller receives a status and a body.
//
// The PATH IS THE CALLER'S, so it is validated here rather than trusted: the
// Desktop proxy resolves it from a closed template allowlist, and this check is
// the second wall under that one.
//
// THE ACCOUNT HEADER COMES FROM THIS TRANSPORT'S OWN SELECTION, not from the
// account argument, which names the account the ROUTE asserts and is empty for
// a caller-scoped route. One mechanism rather than two is deliberate: the
// Desktop proxy already refuses a request whose asserted account is not the
// selected one, so a second, independent stamping path could only ever
// disagree with it. What the argument decides is the KNOWN-INVALID REFUSAL —
// an account-scoped route refuses locally when the gateway has already
// rejected the selection, and a caller-scoped route (/auth/me) bypasses that
// refusal, because a user whose selection was rejected must still be able to
// read their own identity. That is the same asymmetry, and the same reason, as
// [Transport.ListAccounts].
//
// The selection is carried onto the request-scoped transport below, which is
// the one line that separates this method from managementRequest:
// managementRequest always has a fixed account and so never consults a
// selection, while this method would otherwise fall through to the
// PROCESS-WIDE selection at the default config path — the wrong answer for a
// caller whose selection lives in the configuration file it was handed.
func (t *Transport) PlatformRequest(ctx context.Context, call PlatformCall) (PlatformResponse, error) {
	if call.Path == "" || call.Path[0] != '/' || strings.Contains(call.Path, "..") || strings.Contains(call.Path, "//") {
		return PlatformResponse{}, errors.New("invalid platform path")
	}
	// THE QUERY IS NOT THE PATH, and the guard above is the reason it is a
	// separate value. It refuses any path containing ".." or "//", while a URL
	// encoder escapes neither a dot nor a slash inside a value — so a filter
	// the website legitimately accepts (an actor_email of free user text, an
	// RFC3339 timestamp) would be refused as a malformed path if it rode the
	// path argument. What IS checked here is the one property an encoder
	// guarantees: its output carries no unescaped delimiter, so a query naming
	// a second query or a fragment was built by hand and is refused.
	if strings.ContainsAny(call.Query, "?#") {
		return PlatformResponse{}, errors.New("invalid platform query")
	}
	if call.Account != "" && !managementID.MatchString(call.Account) {
		return PlatformResponse{}, errors.New("invalid platform account")
	}
	scoped := NewSyncTransport(t.endpoint, t.source)
	scoped.httpClient = t.httpClient
	scoped.sel = t.sel
	target := call.Path[1:]
	if call.Query != "" {
		target += "?" + call.Query
	}
	response, err := scoped.sendWithAuthBytes(ctx, call.Method, "/", target, jsonAccept, call.Body, call.Account == "")
	if err != nil {
		return PlatformResponse{}, err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, PlatformResponseLimit+1))
	if err != nil {
		return PlatformResponse{}, err
	}
	if len(raw) > PlatformResponseLimit {
		return PlatformResponse{}, ErrPlatformResponseTooLarge
	}
	return PlatformResponse{Status: response.StatusCode, ContentType: response.Header.Get("Content-Type"), Body: raw}, nil
}

func (t *Transport) managementRequest(ctx context.Context, account, method, prefix, suffix string, body []byte) ([]byte, error) {
	scoped := NewSyncTransport(t.endpoint, t.source)
	scoped.httpClient = t.httpClient
	scoped.fixedAccount = account
	response, err := scoped.sendWithAuthBytes(ctx, method, prefix, suffix, jsonAccept, body, false)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, &ManagementHTTPError{Status: response.StatusCode}
	}
	const limit = 4 << 20
	raw, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > limit {
		return nil, errors.New("management response too large")
	}
	return raw, nil
}
