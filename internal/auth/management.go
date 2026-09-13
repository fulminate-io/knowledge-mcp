// SPDX-License-Identifier: Apache-2.0
package auth

import (
	"context"
	"errors"
	"io"
	"net/http"
	"regexp"
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
