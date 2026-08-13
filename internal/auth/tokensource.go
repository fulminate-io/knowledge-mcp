// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
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
// rotated refresh token back to the store on success — retried and
// read-back verified, because the store is how every other process on
// the machine shares this login. A persist that ultimately fails is
// logged at ERROR rather than failing the acquisition.
//
// The zero value is not usable; construct via [NewOAuthTokenSource].
type OAuthTokenSource struct {
	store             Store
	fulminateEndpoint string
	allowedAuthHosts  map[string]struct{}

	mu          sync.Mutex
	endpoints   *DiscoveredEndpoints // lazily populated on first refresh
	accessToken string
	expiresAt   time.Time
	permissions PermissionSet
	warnedOnce  bool
}

// NewOAuthTokenSource wires a store-backed token source. fulminateEndpoint
// is the Fulminate API base URL (no trailing slash, e.g. "https://fulminate.io").
// allowedAuthHosts is the closed set of AuthKit hostnames the CLI trusts as
// authorization servers — discovery refuses to follow a redirect to any host
// outside this set.
//
// There is no clientID argument: the knowledge CLI registers a public client
// dynamically at login (see auth.RegisterPublicClient) and persists the issued
// id under KeyClientID. The refresh path reads it from the store per call.
func NewOAuthTokenSource(
	store Store,
	fulminateEndpoint string,
	allowedAuthHosts map[string]struct{},
) *OAuthTokenSource {
	return &OAuthTokenSource{
		store:             store,
		fulminateEndpoint: fulminateEndpoint,
		allowedAuthHosts:  allowedAuthHosts,
	}
}

// Token implements [TokenSource]. It returns a cached access token if
// one is valid + >5min from expiry; otherwise it reads the refresh
// token from the store, exchanges it for a new access/refresh pair,
// caches the new access token, and then persists the rotated refresh
// token (a persist failure is logged, not fatal — the freshly acquired
// access token is already cached and served).
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

	// The client_id was issued by Dynamic Client Registration at login and
	// persisted alongside the refresh token; a refresh token can only be
	// redeemed by the client it was issued to.
	clientID, err := o.store.Get(ctx, KeyClientID)
	if err != nil {
		return "", nil, fmt.Errorf("auth: load client id: %w", err)
	}

	tr, err := RefreshAccessToken(ctx, eps.TokenEndpoint, clientID, rt, eps.Resource)
	if err != nil {
		// Emitted before warnRefreshFailureOnce, which flips the
		// once-guard the two warnings share.
		o.warnStaleStoredTokenLocked(err)
		o.warnRefreshFailureOnce(err)
		return "", nil, err
	}

	// Cache the validly-acquired access token FIRST, before attempting to
	// persist the rotated refresh token. The access token is good right
	// now regardless of whether the store write below succeeds, and a
	// headless/detached daemon whose keychain write fails must still be
	// able to serve the token it just obtained rather than re-refreshing
	// on every call. This also resets warnedOnce (via
	// populateFromResponseLocked) — unchanged semantics.
	o.populateFromResponseLocked(tr)

	if tr.RefreshToken != "" && tr.RefreshToken != rt {
		if attempts, persistErr := o.persistRotatedTokenLocked(ctx, tr.RefreshToken); persistErr != nil {
			// Availability is unchanged: the access token acquired above is
			// already cached and is returned regardless of this write. What
			// is not tolerable is silence — a silent persist failure strands
			// every sibling process on a consumed token, because the
			// provider rotates the refresh token on every use and only the
			// replacement can be redeemed.
			slog.Error("auth: could not persist the rotated refresh token — the stored credential is now stale; other knowledge processes on this machine will fail to authenticate until this process persists a rotation or `knowledge login` is rerun",
				"error", persistErr, "attempts", attempts)
		}
	}

	// Publish the session so read-only consumers — processes that may use
	// this login but must never write the store — can serve requests without
	// refreshing. Best-effort and separate from the rotation persist above:
	// this process already holds the token in memory, so a failure costs
	// those readers a session, not this caller its token.
	if err := PublishSessionToken(ctx, o.store, tr); err != nil {
		slog.Warn("auth: could not publish the session access token — read-only consumers on this machine will see no usable session until the next successful publish",
			"error", err)
	}

	return o.accessToken, o.permissions, nil
}

// PublishSessionToken records the current session's access token and the
// instant it stops being usable, under [KeyAccessToken] and
// [KeyAccessTokenExpiry].
//
// Called by whichever process owns the session — the login command and the
// refreshing token source — on the write paths those already perform, so
// publishing adds no new writer to the store. [ReadOnlyTokenSource] is the
// consumer.
//
// The expiry comes from the token's own `exp` claim, falling back to the
// response's expires_in when the claim cannot be read. A session whose expiry
// resolves to neither is written as the zero instant, which every reader
// treats as expired — publishing a token nobody can date is worse than
// publishing none.
func PublishSessionToken(ctx context.Context, store Store, tr *TokenResponse) error {
	_, exp, err := ParsePermissionsFromJWT(tr.AccessToken)
	if err != nil || exp.IsZero() {
		exp = computeExpiry(tr)
	}
	if err := store.Set(ctx, KeyAccessToken, tr.AccessToken); err != nil {
		return fmt.Errorf("auth: publish session token: %w", err)
	}
	if err := store.Set(ctx, KeyAccessTokenExpiry, exp.UTC().Format(time.RFC3339)); err != nil {
		return fmt.Errorf("auth: publish session token expiry: %w", err)
	}
	return nil
}

// persistRetryDelays are the waits between rotation-persist attempts —
// one entry per RETRY, so the store is written at most
// len(persistRetryDelays)+1 times. Deliberately short: the refresh path
// holds o.mu across these waits, and they are only reached once a write
// has already failed.
var persistRetryDelays = []time.Duration{
	50 * time.Millisecond,
	150 * time.Millisecond,
}

// errPersistReadBack reports that a store write returned success but the
// value did not survive a read-back. It carries no token material.
var errPersistReadBack = errors.New(
	"auth: rotated refresh token did not survive read-back",
)

// persistRotatedTokenLocked writes the rotated refresh token to the store
// and confirms it landed, retrying briefly on failure. It returns the
// number of write attempts made and the final error, nil once a write is
// confirmed.
//
// Caller must hold o.mu.
func (o *OAuthTokenSource) persistRotatedTokenLocked(
	ctx context.Context, token string,
) (int, error) {
	for attempt := 0; ; attempt++ {
		err := o.setAndVerifyLocked(ctx, token)
		if err == nil {
			return attempt + 1, nil
		}
		if attempt >= len(persistRetryDelays) {
			return attempt + 1, err
		}
		timer := time.NewTimer(persistRetryDelays[attempt])
		select {
		case <-ctx.Done():
			timer.Stop()
			return attempt + 1, errors.Join(err, ctx.Err())
		case <-timer.C:
		}
	}
}

// setAndVerifyLocked writes the token and reads it back, treating a value
// that does not match what was written as a failed write: a Set that
// reports success without the value landing is, from every other
// process's point of view, indistinguishable from no write at all.
//
// Caller must hold o.mu.
func (o *OAuthTokenSource) setAndVerifyLocked(ctx context.Context, token string) error {
	if err := o.store.Set(ctx, KeyRefreshToken, token); err != nil {
		return err
	}
	stored, err := o.store.Get(ctx, KeyRefreshToken)
	if err != nil {
		return fmt.Errorf("auth: read back rotated refresh token: %w", err)
	}
	if stored != token {
		return errPersistReadBack
	}
	return nil
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

// warnStaleStoredTokenLocked leaves a breadcrumb when the refresh token
// read from the store is rejected as no longer redeemable. The provider
// rotates the refresh token on every use, so the usual cause is that
// another process on this machine rotated it and could not persist the
// replacement. The error returned to the caller is unchanged.
//
// Shares the warnedOnce guard with warnRefreshFailureOnce, which must be
// called after this one. Caller must hold o.mu.
func (o *OAuthTokenSource) warnStaleStoredTokenLocked(err error) {
	if o.warnedOnce || !isCredentialRejected(err) {
		return
	}
	slog.Warn("auth: the stored refresh token was rejected — it appears already consumed, which happens when another process rotated it and could not persist the replacement; rerun `knowledge login` to reset it",
		"error", err)
}

// isCredentialRejected reports whether a refresh failure is the server
// rejecting the credential itself — invalid_grant, or any 401 — rather
// than a transport or server-side fault.
func isCredentialRejected(err error) bool {
	if errors.Is(err, ErrInvalidGrant) {
		return true
	}
	var oe *OAuthError
	return errors.As(err, &oe) && oe.StatusCode == http.StatusUnauthorized
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
