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
// as a generic password under (service, key).
//
// The service is carried as a field rather than read from the ServiceName
// const at each call: it is resolved once, when the store is opened, from the
// credential namespace, so every operation this store performs names one
// service and no call site can derive a different one.
type darwinStore struct {
	service string
}

// NewStore returns the platform-appropriate Store implementation for the
// given keychain service identifier, which [OpenStore] derives from the
// credential namespace.
//
// On darwin the keychain is always available (the `security` CLI ships with
// macOS), so outside a test binary NewStore never fails today. Inside one it
// always fails: tests must use in-memory fakes; the real credential store is
// off-limits to test binaries.
func NewStore(service string) (Store, error) {
	if err := refuseRealStoreInTest(); err != nil {
		return nil, err
	}
	return darwinStore{service: service}, nil
}

// Get retrieves a secret from the Keychain. Returns ErrNotFound if the key is
// absent. The context is accepted for API compatibility but not honored —
// the underlying `security` invocation is synchronous.
func (s darwinStore) Get(_ context.Context, key string) (string, error) {
	v, err := keyring.Get(s.service, key)
	if err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return "", ErrNotFound
		}
		return "", fmt.Errorf("auth: keychain get %q: %w", key, err)
	}
	return v, nil
}

// Set writes a secret to the Keychain, creating or overwriting the entry.
func (s darwinStore) Set(_ context.Context, key, value string) error {
	if err := keyring.Set(s.service, key, value); err != nil {
		return fmt.Errorf("auth: keychain set %q: %w", key, err)
	}
	return nil
}

// Delete removes a secret from the Keychain. Returns ErrNotFound if the key
// is absent.
func (s darwinStore) Delete(_ context.Context, key string) error {
	if err := keyring.Delete(s.service, key); err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return ErrNotFound
		}
		return fmt.Errorf("auth: keychain delete %q: %w", key, err)
	}
	return nil
}
