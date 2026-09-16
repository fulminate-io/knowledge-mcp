// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"os"
	"strings"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/auth"
)

// TestCheckFulminateAuth_MalformedNamespaceIsAnError pins `doctor`'s row for a
// malformed selector: the severity doctor already uses for bad configuration,
// with the refusal itself as the message.
//
// The second arm is the regression guard that makes the first meaningful: every
// OTHER store error keeps statusInfo and its present message, which is what
// checkFulminateAuth reserves info for — paid features being unavailable is a
// fully-supported state, and a bad environment variable is not that state.
func TestCheckFulminateAuth_MalformedNamespaceIsAnError(t *testing.T) {
	t.Run("a-malformed-selector-is-a-configuration-error", func(t *testing.T) {
		t.Setenv(auth.CredentialNamespaceEnv, "Dev")

		got := checkFulminateAuth()
		if got.status != statusErr {
			t.Fatalf("doctor status = %v, want statusErr (%v)", got.status, statusErr)
		}
		for _, want := range []string{auth.CredentialNamespaceEnv, `"Dev"`} {
			if !strings.Contains(got.msg, want) {
				t.Errorf("doctor message %q does not name %q", got.msg, want)
			}
		}
	})

	// Known-positive control through the same instrument: inside a test binary
	// the real store constructor refuses, so this arm is a genuine
	// store-construction error that is NOT the selector.
	t.Run("control-any-other-store-error-stays-info", func(t *testing.T) {
		if err := os.Unsetenv(auth.CredentialNamespaceEnv); err != nil {
			t.Fatalf("unset %s: %v", auth.CredentialNamespaceEnv, err)
		}

		got := checkFulminateAuth()
		if got.status != statusInfo {
			t.Fatalf("doctor status for a plain store error = %v, want statusInfo (%v)", got.status, statusInfo)
		}
		if !strings.HasPrefix(got.msg, "credential store unavailable: ") {
			t.Errorf("doctor message = %q, want the unchanged \"credential store unavailable: \" prefix", got.msg)
		}
	})
}
