// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"errors"
	"os/exec"
	"syscall"
)

// newKeychainStoreFn is the platform keychain constructor OpenStore selects
// through. It exists so tests can inject a keychain that fails with a chosen
// error class: the real backend is unavailable on CI runners, and on a
// developer machine there is no other way to produce a keychain error without
// touching the developer's own keychain.
var newKeychainStoreFn = NewStore

// isBackendUnavailable reports whether err proves the platform keychain is
// not reachable at all, as opposed to reachable and refusing.
//
// The allowlist is closed and deny-by-default: an unrecognized error is NOT
// unavailability, so a keychain that is present but unhappy surfaces its
// error instead of silently downgrading a credential to a plaintext file.
//
// Members:
//   - exec.ErrNotFound — the in-container case, where go-keyring's Linux
//     backend shells out to dbus-launch and the binary is absent. go-keyring
//     passes godbus's error through unwrapped and linuxStore wraps it with
//     %w, so errors.Is reaches it.
//   - syscall.ENOENT / syscall.ECONNREFUSED — dbus is present but the
//     session-bus socket is missing or dead.
//
// A denied macOS keychain prompt can never classify as unavailable, and that
// holds by error TYPE rather than by convention: go-keyring's darwin backend
// shells /usr/bin/security and returns the raw *exec.ExitError on a non-zero
// exit, which matches no member of this list.
//
// ErrNotImplementedOS is deliberately absent. Including it would enable the
// file store on Windows, where os.Chmod toggles only the read-only bit, so a
// 0600 credential file would not actually be owner-restricted — the file
// store's central security property would not hold. Leaving it out keeps
// Windows behavior exactly as it is today.
func isBackendUnavailable(err error) bool {
	if err == nil || errors.Is(err, ErrNotFound) {
		// "The key is not there" proves the backend works.
		return false
	}
	return errors.Is(err, exec.ErrNotFound) ||
		errors.Is(err, syscall.ENOENT) ||
		errors.Is(err, syscall.ECONNREFUSED)
}

// OpenStore returns the credential store to use in this process: the one
// selectStore picks, wrapped read-only when [CredentialStoreReadOnlyEnv] is
// set so reads still work and every write refuses.
//
// The wrap is applied HERE, to whichever store was selected, rather than
// inside a backend — the rule is "this process must not write the operator's
// credentials", and it would not hold if the keychain refused writes while
// the file fallback accepted them.
//
// The signature matches NewStore so the existing construction seams and their
// test fakes keep compiling unchanged.
func OpenStore() (Store, error) {
	store, err := selectStore()
	if err != nil {
		return nil, err
	}
	if CredentialStoreIsReadOnly() {
		return readOnlyStore{store}, nil
	}
	return store, nil
}

// selectStore picks the backing credential store for this machine: the
// platform keychain whenever it is reachable, and the file-backed fallback
// only when the keychain is provably unreachable.
//
// The keychain wins whenever it works, even if a credentials file also exists
// on disk, so the fallback can never shadow a real keychain entry. A keychain
// that is reachable but failing is returned as-is, letting the real error
// surface on the caller's next operation rather than being swallowed by a
// silent downgrade to plaintext. Windows takes that branch: its stub
// constructs without error and every operation returns ErrNotImplementedOS,
// which the CLI already handles.
func selectStore() (Store, error) {
	ks, err := newKeychainStoreFn()
	if err != nil {
		if isBackendUnavailable(err) {
			return newFileStore()
		}
		return nil, err
	}
	// Probe once. KeyClientID rather than KeyRefreshToken so a keychain
	// holding only a stale refresh token probes identically.
	_, perr := ks.Get(context.Background(), KeyClientID)
	if perr == nil || errors.Is(perr, ErrNotFound) {
		return ks, nil
	}
	if isBackendUnavailable(perr) {
		return newFileStore()
	}
	return ks, nil
}
