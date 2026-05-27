// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// TokenSource abstracts over "how do I get a valid access token + the
// permissions it carries right now?" It hides the details of cache
// hits, refresh calls, and keychain persistence from callers.
//
// Implementations must be safe for concurrent use — paid-feature
// dispatch may invoke Token from multiple request goroutines.
type TokenSource interface {
	// Token returns a currently-valid access token and the permissions
	// it carries. Implementations may block briefly to refresh the
	// token if the cached copy is near expiry. Returns an error only
	// when no token can be obtained (refresh revoked, network down with
	// no cache, etc.).
	Token(ctx context.Context) (accessToken string, perms PermissionSet, err error)
}

// RefreshingTokenSource is an optional capability that a [TokenSource]
// may implement to allow callers to force a refresh even when the
// cached token has not yet expired. The sync transport uses this on
// HTTP 401 responses: the agent may have rotated signing keys or
// invalidated a token early, in which case the client's locally-cached
// (still-unexpired) token is stale and the only way to recover without
// re-login is to force a refresh and retry.
//
// Implementations that cannot refresh (e.g. [StaticTokenSource]) simply
// do not implement this interface; callers then skip the retry and
// surface the 401 to the user.
type RefreshingTokenSource interface {
	TokenSource
	// ForceRefresh discards any cached access token and obtains a fresh
	// one. Returns the new token + permissions or a terminal error
	// (refresh revoked, network down, etc.). Safe for concurrent use.
	ForceRefresh(ctx context.Context) (accessToken string, perms PermissionSet, err error)
}

// StaticTokenSource is a zero-IO [TokenSource] that always returns the
// same token + permissions. It exists for tests and for edge cases
// where the caller has acquired a token out of band (e.g. environment
// variable).
//
// The field is named AccessToken (not Token) to avoid colliding with
// the Token method on the [TokenSource] interface.
type StaticTokenSource struct {
	AccessToken string
	Permissions PermissionSet
}

// Token implements [TokenSource]. The error return is always nil.
func (s StaticTokenSource) Token(_ context.Context) (string, PermissionSet, error) {
	return s.AccessToken, s.Permissions, nil
}

// accessCacheMargin is how far ahead of the token's `exp` claim we
// proactively refresh. 5 minutes comfortably covers clock skew between
// the client and AuthKit plus any in-flight request that started just
// before the cache-hit check.
const accessCacheMargin = 5 * time.Minute

// OAuthTokenSource is a [TokenSource] that manages the full WorkOS
// AuthKit access-token lifecycle. It caches the current access token
// in memory, loads the long-lived refresh token from a [Store] when it
// needs to refresh, runs RFC 9728 + RFC 8414 discovery against the
// Fulminate endpoint to learn AuthKit's token URL, and persists the
// rotated refresh token back to the store on every success.
//
// The zero value is not usable; construct via [NewOAuthTokenSource].
type OAuthTokenSource struct {
	store             Store
	fulminateEndpoint string
	clientID          string
	allowedAuthHosts  map[string]struct{}

	mu          sync.Mutex
	endpoints   *DiscoveredEndpoints // lazily populated on first refresh
	accessToken string
	expiresAt   time.Time
	permissions PermissionSet
	warnedOnce  bool
}

// NewOAuthTokenSource wires a store-backed token source. fulminateEndpoint
// is the Fulminate API base URL (no trailing slash, e.g. "https://fulminate.io");
// clientID is the OAuth client identifier ("knowledge-cli" for the
// knowledge binary). allowedAuthHosts is the closed set of AuthKit
// hostnames the CLI trusts as authorization servers — discovery refuses
// to follow a redirect to any host outside this set.
func NewOAuthTokenSource(
	store Store,
	fulminateEndpoint, clientID string,
	allowedAuthHosts map[string]struct{},
) *OAuthTokenSource {
	return &OAuthTokenSource{
		store:             store,
		fulminateEndpoint: fulminateEndpoint,
		clientID:          clientID,
		allowedAuthHosts:  allowedAuthHosts,
	}
}

// Token implements [TokenSource]. It returns a cached access token if
// one is valid + >5min from expiry; otherwise it reads the refresh
// token from the store, exchanges it for a new access/refresh pair,
// persists the rotated refresh token, and caches the new access token.
//
// On [ErrInvalidGrant] (refresh revoked/expired) the persisted token is
// NOT cleared — that is the caller's decision (a `knowledge logout`
// command explicitly deletes the keychain entry). Token surfaces the
// error so the caller can surface a re-login prompt.
func (o *OAuthTokenSource) Token(ctx context.Context) (string, PermissionSet, error) {
	o.mu.Lock()
	defer o.mu.Unlock()

	if o.accessToken != "" && time.Until(o.expiresAt) > accessCacheMargin {
		return o.accessToken, o.permissions, nil
	}
	return o.refreshLocked(ctx)
}

// ForceRefresh implements [RefreshingTokenSource]. It unconditionally
// invalidates the in-memory cache and exchanges the stored refresh
// token for a new access token. Callers use this path on HTTP 401
// responses to recover from agent-side revocation without restarting
// the browser flow.
//
// The cache is cleared BEFORE the refresh call so that if the refresh
// itself fails the caller can't accidentally read a stale token on the
// next Token() call.
func (o *OAuthTokenSource) ForceRefresh(ctx context.Context) (string, PermissionSet, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.accessToken = ""
	o.expiresAt = time.Time{}
	o.permissions = nil
	return o.refreshLocked(ctx)
}

// ensureEndpointsLocked runs RFC 9728 + RFC 8414 discovery if it hasn't
// already, caching the result on the receiver. Caller must hold o.mu.
func (o *OAuthTokenSource) ensureEndpointsLocked(ctx context.Context) (*DiscoveredEndpoints, error) {
	if o.endpoints != nil {
		return o.endpoints, nil
	}
	eps, err := Discover(ctx, o.fulminateEndpoint, o.allowedAuthHosts)
	if err != nil {
		return nil, fmt.Errorf("auth: discover for refresh: %w", err)
	}
	o.endpoints = eps
	return eps, nil
}

// refreshLocked performs the refresh flow. The caller must hold o.mu.
// Extracted so Token stays short and the mutex contract is explicit.
func (o *OAuthTokenSource) refreshLocked(ctx context.Context) (string, PermissionSet, error) {
	eps, err := o.ensureEndpointsLocked(ctx)
	if err != nil {
		o.warnRefreshFailureOnce(err)
		return "", nil, err
	}

	rt, err := o.store.Get(ctx, KeyRefreshToken)
	if err != nil {
		return "", nil, fmt.Errorf("auth: load refresh token: %w", err)
	}

	tr, err := RefreshAccessToken(ctx, eps.TokenEndpoint, o.clientID, rt, eps.Resource)
	if err != nil {
		o.warnRefreshFailureOnce(err)
		return "", nil, err
	}

	if tr.RefreshToken != "" && tr.RefreshToken != rt {
		if setErr := o.store.Set(ctx, KeyRefreshToken, tr.RefreshToken); setErr != nil {
			// Persistence failed. The new access token is still usable
			// until expiry but without the rotated refresh token we're
			// locked out after that. Return the error so the caller
			// (CLI, logger, metrics) can see this rather than silently
			// accepting an un-rotatable credential state.
			return "", nil, fmt.Errorf("auth: persist rotated refresh token: %w", setErr)
		}
	}

	o.populateFromResponseLocked(tr)
	return o.accessToken, o.permissions, nil
}

// populateFromResponseLocked caches the new access token, permission
// set, and expiry from the JWT claims. The caller must hold o.mu.
//
// WorkOS access tokens carry permissions in the `permissions` claim and
// expiry in `exp`. The TokenResponse.ExpiresIn field is used only as a
// fallback when the JWT can't be parsed (which would be a server-side
// bug; the access_token is always a JWT in WorkOS).
func (o *OAuthTokenSource) populateFromResponseLocked(tr *TokenResponse) {
	o.accessToken = tr.AccessToken

	perms, exp, err := ParsePermissionsFromJWT(tr.AccessToken)
	if err == nil {
		o.permissions = perms
		if !exp.IsZero() {
			o.expiresAt = exp
		} else {
			o.expiresAt = computeExpiry(tr)
		}
	} else {
		o.permissions = nil
		o.expiresAt = computeExpiry(tr)
	}
	o.warnedOnce = false
}

// computeExpiry picks a wall-clock expiry from the TokenResponse's
// ExpiresIn field (seconds until expiry). Falls back to "unknown" (zero
// time) if the field is missing so the caller treats the cache as
// already stale and forces a refresh on next call.
func computeExpiry(tr *TokenResponse) time.Time {
	if tr.ExpiresIn <= 0 {
		return time.Time{}
	}
	return time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second)
}

// warnRefreshFailureOnce emits a single WARN log per session when a
// refresh attempt fails. Reset on successful refresh so a later failure
// surfaces a fresh warning.
//
// Caller must hold o.mu.
func (o *OAuthTokenSource) warnRefreshFailureOnce(err error) {
	if o.warnedOnce {
		return
	}
	o.warnedOnce = true
	hint := "rerun `knowledge login` to re-authenticate"
	if errors.Is(err, ErrInvalidGrant) {
		hint = "refresh token revoked or expired — rerun `knowledge login`"
	}
	slog.Warn("auth: refresh failed — paid features disabled for this session",
		"error", err, "hint", hint)
}
