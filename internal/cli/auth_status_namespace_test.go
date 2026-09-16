// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"errors"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/auth"
)

// TestAuthStatusCmd_MalformedNamespaceStaysIndeterminate is the LOUD disposition
// class's behavioral row: the five openers that already surfaced a store error
// (auth-status, login, logout, tunnel, BuildSyncTransport) share one code path —
// cli.openStore wrapping auth.OpenStore's error with %w — so one verb covers the
// class, and auth-status is the one with a documented exit-code contract to
// keep.
//
// That contract (authStatusUsage, auth_status.go) distinguishes "cannot ask"
// from "the answer is no": exit 1 is indeterminate, exit 2 is a definitive
// no-valid-session. A malformed selector is the former. It is asserted here
// BOTH ways, because only the pair is the contract: the refusal must reach the
// caller as the namespace sentinel, and it must NOT be ErrNoValidSession, which
// the dispatcher maps to ExitNoValidSession.
//
// The store seam is left at its DEFAULT so the real auth.OpenStore runs. It
// refuses the value before constructing anything, so nothing here touches a
// keychain or a credentials file.
func TestAuthStatusCmd_MalformedNamespaceStaysIndeterminate(t *testing.T) {
	t.Setenv(auth.CredentialNamespaceEnv, "Dev")

	err := AuthStatusCmd(nil)
	if err == nil {
		t.Fatal("auth-status under a malformed selector returned nil — a broken credential configuration must not read as a valid session")
	}
	if !errors.Is(err, auth.ErrCredentialNamespaceInvalid) {
		t.Fatalf("auth-status returned %v, want the refusal to surface as ErrCredentialNamespaceInvalid", err)
	}
	if errors.Is(err, ErrNoValidSession) {
		t.Fatalf("auth-status mapped a malformed selector to ErrNoValidSession (exit %d) — that is the definitive \"not logged in\" answer, and a selector we could not parse is a question we could not ask", ExitNoValidSession)
	}

	// Known-positive control on the same instrument: a WELL-FORMED selector
	// leaves the verb on its ordinary path, so the rows above discriminate on
	// the value rather than on the variable being present. Inside a test binary
	// the real store constructor refuses, so this arm is a plain store error —
	// still indeterminate, still not a refusal.
	t.Run("control-a-valid-selector-is-not-the-namespace-refusal", func(t *testing.T) {
		t.Setenv(auth.CredentialNamespaceEnv, "dev")

		err := AuthStatusCmd(nil)
		if err == nil {
			t.Fatal("auth-status returned nil inside a test binary, where the real store constructor refuses")
		}
		if errors.Is(err, auth.ErrCredentialNamespaceInvalid) {
			t.Fatalf("auth-status refused a well-formed selector: %v", err)
		}
		if errors.Is(err, ErrNoValidSession) {
			t.Fatalf("auth-status mapped a store error to ErrNoValidSession (exit %d), which is the arm reserved for a definitive answer", ExitNoValidSession)
		}
	})
}
