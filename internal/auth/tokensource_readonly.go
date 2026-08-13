// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ErrNoSession reports that no session access token is stored: nobody has
// logged in on this machine, or the stored session was cleared.
var ErrNoSession = errors.New(
	"auth: not authenticated — run `knowledge login`",
)

// ErrSessionExpired reports that a session access token is stored but is no
// longer usable. A read-only consumer cannot fix this itself: only the
// process that owns the session can refresh it.
var ErrSessionExpired = errors.New(
	"auth: session token expired — the owning session must refresh it, " +
		"or run `knowledge login`",
)

// ReadOnlyTokenSource is a [TokenSource] that serves the session access token
// published in a [Store] by whichever process owns the session, and can do
// nothing else. It is for processes that must use an existing login without
// modifying it — a test harness driving a real binary against the operator's
// machine being the motivating case.
//
// It is read-only BY CONSTRUCTION, not by convention: there is no store write
// anywhere in its path and no OAuth client behind it, so it cannot mint,
// rotate, or persist a credential even if a caller wanted it to. It
// deliberately does NOT implement [RefreshingTokenSource], so a caller
// handling an HTTP 401 surfaces the error instead of spinning a
// force-refresh retry this source could never satisfy.
//
// Pair it with [CredentialStoreReadOnlyEnv] for defense in depth: this type
// cannot write, and under that lever the store beneath it would refuse the
// write anyway.
type ReadOnlyTokenSource struct {
	store Store
}

// NewReadOnlyTokenSource wires a read-only source over the given store. The
// store is used for reads only.
func NewReadOnlyTokenSource(store Store) *ReadOnlyTokenSource {
	return &ReadOnlyTokenSource{store: store}
}

// Token implements [TokenSource]. It returns the published session token when
// one is stored and still valid, [ErrNoSession] when none is stored, and
// [ErrSessionExpired] when the stored one has lapsed.
//
// It never refreshes and never writes: an expired session is a terminal error
// for this source, reported so the caller can say who must act.
func (s *ReadOnlyTokenSource) Token(ctx context.Context) (string, PermissionSet, error) {
	token, err := s.read(ctx, KeyAccessToken)
	if err != nil {
		return "", nil, err
	}
	if token == "" {
		return "", nil, ErrNoSession
	}

	raw, err := s.read(ctx, KeyAccessTokenExpiry)
	if err != nil {
		return "", nil, err
	}
	expiry, parseErr := time.Parse(time.RFC3339, raw)
	if parseErr != nil {
		// Fail closed: an expiry that cannot be read is not evidence that
		// the token is still good.
		return "", nil, ErrSessionExpired
	}
	if !time.Now().Before(expiry) {
		return "", nil, ErrSessionExpired
	}

	// Permissions ride in the token's own claims. A token that will not parse
	// still serves — the server is the authority on what it grants — so the
	// parse error is deliberately discarded: the helper yields an empty set
	// alongside it, which is the intended degraded value here and matches
	// [OAuthTokenSource].
	perms, _, _ := ParsePermissionsFromJWT(token)
	return token, perms, nil
}

// read fetches one session key, mapping absence to [ErrNoSession] so a
// half-written session reads the same as no session at all.
func (s *ReadOnlyTokenSource) read(ctx context.Context, key string) (string, error) {
	v, err := s.store.Get(ctx, key)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return "", ErrNoSession
		}
		return "", fmt.Errorf("auth: read %s: %w", key, err)
	}
	return v, nil
}
