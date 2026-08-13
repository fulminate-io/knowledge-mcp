// SPDX-License-Identifier: Apache-2.0

//go:build linux

package auth

import (
	"context"
	"errors"
	"fmt"

	keyring "github.com/zalando/go-keyring"
)

// linuxStore persists secrets via the freedesktop SecretService (libsecret
// over dbus), wrapped by zalando/go-keyring. The library's Linux backend is
// pure Go (godbus), so no CGO is required and the package cross-compiles.
// At runtime a SecretService-compatible daemon (gnome-keyring-daemon,
// kwallet's Secret Service API, KeePassXC, etc.) must be running on the
// user's session bus.
type linuxStore struct{}

// NewStore returns the platform-appropriate Store implementation.
//
// On linux a successful construction does not guarantee the session bus or
// secret daemon are actually reachable — those errors surface on the first
// Set/Get/Delete call. We intentionally defer the probe because there is no
// cheap "is it available?" check that doesn't also touch the keyring.
//
// Inside a test binary construction always fails: tests must use in-memory
// fakes; the real credential store is off-limits to test binaries.
func NewStore() (Store, error) {
	if err := refuseRealStoreInTest(); err != nil {
		return nil, err
	}
	return linuxStore{}, nil
}

// Get retrieves a secret from the SecretService. Returns ErrNotFound if the
// key is absent.
func (linuxStore) Get(_ context.Context, key string) (string, error) {
	v, err := keyring.Get(ServiceName, key)
	if err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return "", ErrNotFound
		}
		return "", fmt.Errorf("auth: secretservice get %q: %w", key, err)
	}
	return v, nil
}

// Set writes a secret to the SecretService, creating or overwriting the
// entry under (ServiceName, key).
func (linuxStore) Set(_ context.Context, key, value string) error {
	if err := keyring.Set(ServiceName, key, value); err != nil {
		return fmt.Errorf("auth: secretservice set %q: %w", key, err)
	}
	return nil
}

// Delete removes a secret from the SecretService. Returns ErrNotFound if the
// key is absent.
func (linuxStore) Delete(_ context.Context, key string) error {
	if err := keyring.Delete(ServiceName, key); err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return ErrNotFound
		}
		return fmt.Errorf("auth: secretservice delete %q: %w", key, err)
	}
	return nil
}
