// SPDX-License-Identifier: Apache-2.0
package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type publishedReadFailure struct {
	Store
	key string
	err error
}

func (s publishedReadFailure) Get(ctx context.Context, key string) (string, error) {
	if key == s.key {
		return "", s.err
	}
	return s.Store.Get(ctx, key)
}

func TestOAuthPublishedSession(t *testing.T) {
	for _, tc := range []struct {
		name, token, expiry string
		refresh             bool
		failKey             string
		wantErr             error
	}{
		{name: "opaque", token: "opaque", expiry: "2099-01-02T03:04:05Z"},
		{name: "jwt", token: "jwt", expiry: "2099-01-02T03:04:05Z"},
		{name: "absent", refresh: true},
		{name: "expired", token: "opaque", expiry: "2000-01-02T03:04:05Z", refresh: true},
		{name: "margin", token: "opaque", expiry: time.Now().Add(time.Minute).Format(time.RFC3339), refresh: true},
		{name: "malformed", token: "opaque", expiry: "bad", wantErr: ErrSessionExpired},
		{name: "access read error", failKey: KeyAccessToken, wantErr: errors.New("store unavailable")},
		{name: "expiry read error", token: "opaque", failKey: KeyAccessTokenExpiry, wantErr: errors.New("store unavailable")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			fresh := signTestJWT(t, []string{PermMCPKnowledgeRead}, time.Now().Add(time.Hour).Unix())
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.Header().Set("Content-Type", "application/json")
				//nolint:gosec // Loopback fixture response contains only generated test credentials.
				_ = json.NewEncoder(w).Encode(TokenResponse{AccessToken: fresh, RefreshToken: "next", ExpiresIn: 3600})
			}))
			defer srv.Close()
			store := newTestStore()
			ctx := context.Background()
			_ = store.Set(ctx, KeyRefreshToken, "refresh")
			if tc.token == "jwt" {
				tc.token = signTestJWT(t, []string{PermMCPKnowledgeRead}, 4071006245)
			}
			if tc.token != "" {
				_ = store.Set(ctx, KeyAccessToken, tc.token)
				_ = store.Set(ctx, KeyAccessTokenExpiry, tc.expiry)
			}
			src := newOAuthSourceForTest(store, srv.URL)
			if tc.failKey != "" {
				src.store = publishedReadFailure{Store: store, key: tc.failKey, err: tc.wantErr}
			}
			got, perms, err := src.Token(ctx)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("error=%v want=%v", err, tc.wantErr)
			}
			wantCalls := 0
			if tc.refresh {
				wantCalls = 1
			}
			if calls != wantCalls {
				t.Errorf("refresh calls=%d want=%d", calls, wantCalls)
			}
			if tc.wantErr != nil {
				return
			}
			want := tc.token
			if tc.refresh {
				want = fresh
			}
			if got != want {
				t.Error("wrong access token")
			}
			if tc.name == "opaque" && len(perms) != 0 {
				t.Error("opaque token acquired permissions")
			}
			if tc.name == "jwt" && !perms.Has(PermMCPKnowledgeRead) {
				t.Error("JWT permission missing")
			}
		})
	}
}

func TestOAuthPublishedForceRefresh(t *testing.T) {
	for _, terminal := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "terminal"}[terminal], func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if terminal {
					w.WriteHeader(400)
					_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
					return
				}
				//nolint:gosec // Loopback fixture response contains only generated test credentials.
				_ = json.NewEncoder(w).Encode(TokenResponse{AccessToken: "new-access", RefreshToken: "next", ExpiresIn: 3600})
			}))
			defer srv.Close()
			store := newTestStore()
			ctx := context.Background()
			_ = store.Set(ctx, KeyAccessToken, "rejected")
			_ = store.Set(ctx, KeyAccessTokenExpiry, "2099-01-02T03:04:05Z")
			_ = store.Set(ctx, KeyRefreshToken, "refresh")
			src := newOAuthSourceForTest(store, srv.URL)
			got, _, err := src.ForceRefresh(ctx)
			if terminal {
				if !errors.Is(err, ErrInvalidGrant) {
					t.Fatal(err)
				}
				got, _, err = src.Token(ctx)
				if !errors.Is(err, ErrInvalidGrant) || got != "" || calls != 2 {
					t.Fatal("reloaded rejected publication")
				}
			} else if err != nil || got != "new-access" || calls != 1 {
				t.Fatal("forced refresh did not replace publication")
			}
		})
	}
}
