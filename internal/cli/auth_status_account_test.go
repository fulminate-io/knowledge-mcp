// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/auth"
	"github.com/fulminate-io/knowledge-mcp/internal/config"
)

// TestAuthStatusCmd_ReportsSelectedAccount proves auth-status names the stored
// account when one is selected and says so explicitly when none is — without
// touching the verdict it returns.
func TestAuthStatusCmd_ReportsSelectedAccount(t *testing.T) {
	const id = "acct_01AUTHSTATUSAUTHSTATU"

	seedValidSession := func(t *testing.T) {
		t.Helper()
		rec := withRecordingStore(t)
		ctx := context.Background()
		if err := rec.fakeAuthStore.Set(ctx, auth.KeyAccessToken, "at_live"); err != nil {
			t.Fatalf("seed: %v", err)
		}
		if err := rec.fakeAuthStore.Set(ctx, auth.KeyAccessTokenExpiry,
			time.Now().Add(time.Hour).UTC().Format(time.RFC3339)); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	t.Run("selected account is named", func(t *testing.T) {
		seedValidSession(t)
		path := useHomeWithConfig(t, id)

		out, err := captureStdout(t, func() error { return AuthStatusCmd(nil) })
		if err != nil {
			t.Fatalf("AuthStatusCmd = %v, want nil (the account line must not change the verdict)", err)
		}
		if !strings.Contains(out, "Account: "+id) {
			t.Errorf("output does not name the stored account:\n%s", out)
		}
		// The reported value IS what is stored on disk.
		stored, readErr := config.ReadSelectedAccountID(path)
		if readErr != nil {
			t.Fatalf("ReadSelectedAccountID: %v", readErr)
		}
		if !strings.Contains(out, stored) {
			t.Errorf("output %q does not carry the stored id %q", out, stored)
		}
	})

	t.Run("no selection is stated explicitly", func(t *testing.T) {
		seedValidSession(t)
		useHomeWithConfig(t, "")

		out, err := captureStdout(t, func() error { return AuthStatusCmd(nil) })
		if err != nil {
			t.Fatalf("AuthStatusCmd = %v, want nil", err)
		}
		if !strings.Contains(out, "Account: (none selected") {
			t.Errorf("output does not state that no account is selected:\n%s", out)
		}
		if strings.Contains(out, id) {
			t.Errorf("output leaked an account id that is not stored:\n%s", out)
		}
	})

	t.Run("the account line makes no network call and writes nothing", func(t *testing.T) {
		rec := withRecordingStore(t)
		ctx := context.Background()
		if err := rec.fakeAuthStore.Set(ctx, auth.KeyAccessToken, "at_live"); err != nil {
			t.Fatalf("seed: %v", err)
		}
		if err := rec.fakeAuthStore.Set(ctx, auth.KeyAccessTokenExpiry,
			time.Now().Add(time.Hour).UTC().Format(time.RFC3339)); err != nil {
			t.Fatalf("seed: %v", err)
		}
		useHomeWithConfig(t, id)

		if _, err := captureStdout(t, func() error { return AuthStatusCmd(nil) }); err != nil {
			t.Fatalf("AuthStatusCmd: %v", err)
		}
		if got := rec.recorded(); len(got) != 0 {
			t.Errorf("reporting the account wrote to the credential store: %v", got)
		}
	})
}
