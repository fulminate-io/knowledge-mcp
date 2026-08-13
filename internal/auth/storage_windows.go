// SPDX-License-Identifier: Apache-2.0

//go:build windows

package auth

import "context"

// windowsStore is the placeholder Windows implementation. Every method
// returns ErrNotImplementedOS so callers can detect the "no secret storage
// on this OS" condition.
//
// A real Windows backend (Credential Manager via DPAPI) is planned for a
// future release; until then paid features are inaccessible on Windows.
type windowsStore struct{}

// NewStore returns the platform-appropriate Store implementation.
//
// Outside a test binary the windows stub is always constructed successfully;
// the ErrNotImplementedOS signal is surfaced per-call. Inside one it fails
// like every other platform, so the constructor is one seam on every OS:
// tests must use in-memory fakes; the real credential store is off-limits to
// test binaries.
func NewStore() (Store, error) {
	if err := refuseRealStoreInTest(); err != nil {
		return nil, err
	}
	return windowsStore{}, nil
}

// Get is not implemented on Windows.
func (windowsStore) Get(_ context.Context, _ string) (string, error) {
	return "", ErrNotImplementedOS
}

// Set is not implemented on Windows.
func (windowsStore) Set(_ context.Context, _, _ string) error {
	return ErrNotImplementedOS
}

// Delete is not implemented on Windows.
func (windowsStore) Delete(_ context.Context, _ string) error {
	return ErrNotImplementedOS
}
