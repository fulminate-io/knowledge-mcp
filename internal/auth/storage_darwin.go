// SPDX-License-Identifier: Apache-2.0

//go:build darwin

package auth

import (
	"context"
	"errors"
	"fmt"

	keyring "github.com/zalando/go-keyring"
)

// darwinStore persists secrets in the macOS Keychain via zalando/go-keyring,
// which shells out to /usr/bin/security under the hood. Each entry is stored
// as a generic password under (ServiceName, key).
type darwinStore struct{}

// NewStore returns the platform-appropriate Store implementation.
//
// On darwin the keychain is always available (the `security` CLI ships with
// macOS), so outside a test binary NewStore never fails today. Inside one it
// always fails: tests must use in-memory fakes; the real credential store is
// off-limits to test binaries.
func NewStore() (Store, error) {
	if err := refuseRealStoreInTest(); err != nil {
		return nil, err
	}
	return darwinStore{}, nil
}

// Get retrieves a secret from the Keychain. Returns ErrNotFound if the key is
// absent. The context is accepted for API compatibility but not honored —
// the underlying `security` invocation is synchronous.
func (darwinStore) Get(_ context.Context, key string) (string, error) {
	v, err := keyring.Get(ServiceName, key)
	if err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return "", ErrNotFound
		}
		return "", fmt.Errorf("auth: keychain get %q: %w", key, err)
	}
	return v, nil
}

// Set writes a secret to the Keychain, creating or overwriting the entry.
func (darwinStore) Set(_ context.Context, key, value string) error {
	if err := keyring.Set(ServiceName, key, value); err != nil {
		return fmt.Errorf("auth: keychain set %q: %w", key, err)
	}
	return nil
}

// Delete removes a secret from the Keychain. Returns ErrNotFound if the key
// is absent.
func (darwinStore) Delete(_ context.Context, key string) error {
	if err := keyring.Delete(ServiceName, key); err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return ErrNotFound
		}
		return fmt.Errorf("auth: keychain delete %q: %w", key, err)
	}
	return nil
}
