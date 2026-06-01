// SPDX-License-Identifier: Apache-2.0

// Package auth provides cross-platform secret storage for OAuth credentials
// (refresh tokens) used by the knowledge binary's paid-feature flow.
//
// The package is a leaf utility: it must NOT import any other package from
// this module. Higher-level flows (device-flow client, token refresh
// lifecycle, TokenSource interface) are layered on top in later phases.
//
// Backends are selected at build time via build tags:
//
//   - darwin  → storage_darwin.go  (macOS Keychain via zalando/go-keyring)
//   - linux   → storage_linux.go   (SecretService / libsecret via zalando/go-keyring)
//   - windows → storage_windows.go (stub returning ErrNotImplementedOS)
//
// Tests in this package use the internal `testStore` fixture in
// teststore_test.go. External packages that need a Store fake must construct
// one inline (the interface is small: Get / Set / Delete).
package auth

import (
	"context"
	"errors"
)

// ServiceName is the keychain "service" identifier used for every entry this
// package writes. Per-key entries are differentiated by the "user"/account
// field (see KeyRefreshToken). Keep this stable across releases — changing
// it orphans existing stored credentials.
const ServiceName = "io.fulminate.knowledge"

// KeyRefreshToken is the well-known keychain key for the long-lived OAuth
// refresh token acquired via the device-authorization flow. Only long-lived
// secrets belong in the keychain; short-lived access tokens stay in memory.
const KeyRefreshToken = "refresh_token"

// KeyClientID is the well-known keychain key for the OAuth client_id issued
// to this install by Dynamic Client Registration at login time. WorkOS
// AuthKit honors RFC 8707 resource indicators only for DCR/CIMD clients (a
// hand-created OAuth Application's tokens are minted with aud=client_id and
// reject the resource parameter with invalid_target), so the knowledge CLI
// has no static client_id — it registers a public client during `login` and
// persists the issued id here so the refresh path can reuse it.
const KeyClientID = "client_id"

// Sentinel errors returned by Store implementations. Callers should compare
// via errors.Is — platform backends may wrap these with additional context.
var (
	// ErrNotFound is returned by Get and Delete when the requested key is
	// not present in the backing store.
	ErrNotFound = errors.New("auth: key not found")

	// ErrNotImplementedOS is returned by every method of the Windows stub
	// (and any future platform without a real backend). Callers should treat
	// this as a non-retriable "feature unavailable on this OS" signal —
	// paid features are inaccessible until a real backend is added.
	ErrNotImplementedOS = errors.New(
		"auth: secret storage is not implemented on this OS",
	)
)

// Store is the platform-neutral secret-storage surface. Implementations
// persist key→value pairs under the ServiceName namespace.
//
// All methods take a context so future network- or IPC-backed stores can
// honor cancellation. The current darwin/linux backends complete
// synchronously and ignore the context.
type Store interface {
	// Get returns the value stored under key. It returns ErrNotFound if the
	// key is absent. Any other error indicates a backend failure (dbus,
	// Keychain ACL denial, etc.).
	Get(ctx context.Context, key string) (string, error)

	// Set writes value under key, creating or overwriting the entry.
	Set(ctx context.Context, key, value string) error

	// Delete removes the entry for key. It returns ErrNotFound if the key is
	// not present; callers that want idempotent deletes should ignore that
	// error explicitly.
	Delete(ctx context.Context, key string) error
}
