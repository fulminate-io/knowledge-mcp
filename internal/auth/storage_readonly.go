// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"errors"
	"os"
	"strings"
)

// CredentialStoreReadOnlyEnv names the environment variable that puts this
// process's credential store into read-only mode: reads pass through to the
// real store, writes refuse.
//
// It exists for processes that legitimately need to USE an existing login but
// must never modify it — harness and CI processes must never write the
// operator's credential store. Such a process typically runs a real binary
// rather than a test binary, which is where the compiled testing.Testing()
// guard in storage_testguard.go structurally cannot help.
//
// Read-only rather than forbidden is the whole point: a process that cannot
// read has to authenticate to do its job, and authenticating means rotating
// the refresh token, which means writing. Letting it read is what makes not
// writing possible.
const CredentialStoreReadOnlyEnv = "KNOWLEDGE_CREDENTIAL_STORE_READONLY"

// errCredentialStoreReadOnly is returned by every write method of a store
// opened in read-only mode. It names the variable so the operator can tell
// this apart from a keychain denial.
var errCredentialStoreReadOnly = errors.New(
	"auth: the credential store is read-only in this process (" +
		CredentialStoreReadOnlyEnv + " is set): harness and CI processes " +
		"must never write the operator's credential store",
)

// CredentialStoreIsReadOnly reports whether the read-only lever is engaged.
//
// Any non-empty value engages it except an explicit "0" or "false". A typo'd
// value therefore protects the store rather than silently leaving it
// writable, which is the safe direction for a lever whose whole purpose is to
// prevent a write.
//
// Exported so callers that must change BEHAVIOR under the lever — rather
// than merely be refused by it — can ask. A process that would otherwise
// build a refreshing token source needs to know before it tries, because a
// refresh it cannot persist is worse than one it never attempts. Callers must
// not re-derive this from the environment themselves: one predicate keeps the
// value semantics from drifting between the check and the enforcement.
func CredentialStoreIsReadOnly() bool {
	v := strings.TrimSpace(os.Getenv(CredentialStoreReadOnlyEnv))
	if v == "" {
		return false
	}
	return v != "0" && !strings.EqualFold(v, "false")
}

// readOnlyStore wraps a real [Store] so reads pass through unchanged and
// every write refuses. It embeds the Store interface, so Get is the wrapped
// store's own implementation and only the mutating methods are overridden —
// a store method added later is inherited as a passthrough, so a new WRITE
// method would need overriding here.
type readOnlyStore struct {
	Store
}

// Set refuses: writing is what read-only mode exists to prevent.
func (readOnlyStore) Set(context.Context, string, string) error {
	return errCredentialStoreReadOnly
}

// Delete refuses for the same reason as Set — removing the operator's
// credential is a write like any other.
func (readOnlyStore) Delete(context.Context, string) error {
	return errCredentialStoreReadOnly
}
