// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// TestRefreshAccessToken_Success decodes a rotated refresh+access pair
// and confirms the form-encoded body carries the standard refresh_token
// grant parameters plus the RFC 8707 resource indicator.
func TestRefreshAccessToken_Success(t *testing.T) {
	var seen url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil { //nolint:gosec // test fixture: httptest.Server with trusted local input
			t.Fatalf("parse form: %v", err)
		}
		seen = r.PostForm
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(TokenResponse{ //nolint:gosec // test fixture with literal string values
			AccessToken:  "new.jwt",
			RefreshToken: "frt_new",
			TokenType:    "Bearer",
			ExpiresIn:    900,
		})
	}))
	defer srv.Close()

	tr, err := RefreshAccessToken(context.Background(), srv.URL, "knowledge-cli", "frt_old", "https://fulminate.io/mcp")
	if err != nil {
		t.Fatalf("RefreshAccessToken: %v", err)
	}
	if tr.AccessToken != "new.jwt" || tr.RefreshToken != "frt_new" {
		t.Errorf("unexpected response: %+v", tr)
	}
	if got := seen.Get("grant_type"); got != "refresh_token" {
		t.Errorf("grant_type mismatch: %q", got)
	}
	if got := seen.Get("client_id"); got != "knowledge-cli" {
		t.Errorf("client_id mismatch: %q", got)
	}
	if got := seen.Get("refresh_token"); got != "frt_old" {
		t.Errorf("refresh_token mismatch: %q", got)
	}
	if got := seen.Get("resource"); got != "https://fulminate.io/mcp" {
		t.Errorf("resource mismatch: %q", got)
	}
}

// TestRefreshAccessToken_NoResource asserts an empty resource parameter
// is omitted from the form body (not sent as `resource=`).
func TestRefreshAccessToken_NoResource(t *testing.T) {
	var seen url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil { //nolint:gosec // test fixture
			t.Fatalf("parse form: %v", err)
		}
		seen = r.PostForm
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(TokenResponse{AccessToken: "x", ExpiresIn: 1}) //nolint:gosec // test fixture
	}))
	defer srv.Close()

	if _, err := RefreshAccessToken(context.Background(), srv.URL, "cli", "frt", ""); err != nil {
		t.Fatalf("RefreshAccessToken: %v", err)
	}
	if _, ok := seen["resource"]; ok {
		t.Errorf("expected resource to be omitted, got %q", seen.Get("resource"))
	}
}

// TestRefreshAccessToken_InvalidGrant surfaces the invalid_grant OAuth
// error as ErrInvalidGrant via errors.Is.
func TestRefreshAccessToken_InvalidGrant(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"revoked"}`))
	}))
	defer srv.Close()

	_, err := RefreshAccessToken(context.Background(), srv.URL, "cli", "frt_bad", "")
	if !errors.Is(err, ErrInvalidGrant) {
		t.Fatalf("expected ErrInvalidGrant, got %v", err)
	}
}

// TestRefreshAccessToken_UnexpectedError surfaces unknown OAuth error
// codes as wrapped errors with the server description.
func TestRefreshAccessToken_UnexpectedError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"server_error","error_description":"oops"}`))
	}))
	defer srv.Close()

	_, err := RefreshAccessToken(context.Background(), srv.URL, "cli", "frt_x", "")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if errors.Is(err, ErrInvalidGrant) {
		t.Fatalf("unexpected ErrInvalidGrant: %v", err)
	}
}

// TestRevokeRefreshToken_Success asserts the revocation endpoint is
// called with the expected form body and a 200 response returns nil.
func TestRevokeRefreshToken_Success(t *testing.T) {
	var seen url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil { //nolint:gosec // test fixture
			t.Fatalf("parse form: %v", err)
		}
		seen = r.PostForm
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if err := RevokeRefreshToken(context.Background(), srv.URL, "frt_x"); err != nil {
		t.Fatalf("RevokeRefreshToken: %v", err)
	}
	if got := seen.Get("token"); got != "frt_x" {
		t.Errorf("token mismatch: %q", got)
	}
	if got := seen.Get("token_type_hint"); got != "refresh_token" {
		t.Errorf("token_type_hint mismatch: %q", got)
	}
}

// TestRevokeRefreshToken_EmptyEndpoint asserts the no-op short-circuit
// when AuthKit doesn't advertise a revocation_endpoint.
func TestRevokeRefreshToken_EmptyEndpoint(t *testing.T) {
	if err := RevokeRefreshToken(context.Background(), "", "frt_x"); err != nil {
		t.Fatalf("expected nil on empty endpoint, got %v", err)
	}
}

// TestRevokeRefreshToken_NetworkError asserts transport failures are
// swallowed (returning nil) so local logout can continue.
func TestRevokeRefreshToken_NetworkError(t *testing.T) {
	// Unused address — connection refused.
	if err := RevokeRefreshToken(context.Background(), "http://127.0.0.1:1/revoke", "frt_x"); err != nil {
		t.Fatalf("expected nil on network error, got %v", err)
	}
}

// TestRevokeRefreshToken_Non200 asserts non-200 responses also return
// nil (best-effort semantics).
func TestRevokeRefreshToken_Non200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	if err := RevokeRefreshToken(context.Background(), srv.URL, "frt_x"); err != nil {
		t.Fatalf("expected nil on non-200, got %v", err)
	}
}
