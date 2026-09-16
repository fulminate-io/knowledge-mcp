// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"errors"
	"fmt"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/auth"
)

// TestBuildClient_RefusesAMalformedNamespace is the `serve` verb's own row: the
// daemon does not start. It runs the REAL auth.OpenStore through the untouched
// seam, so it pins the whole path from the environment variable to the startup
// failure rather than an injected error.
func TestBuildClient_RefusesAMalformedNamespace(t *testing.T) {
	t.Setenv(auth.CredentialNamespaceEnv, "Dev")

	c, cleanup, err := buildClient(Config{})
	if !errors.Is(err, auth.ErrCredentialNamespaceInvalid) {
		if cleanup != nil {
			cleanup()
		}
		t.Fatalf("buildClient with a malformed selector = %v, want ErrCredentialNamespaceInvalid", err)
	}
	if c != nil || cleanup != nil {
		t.Errorf("buildClient returned client=%v cleanup=%v alongside the refusal, want neither", c != nil, cleanup != nil)
	}
	// The refusal's WORDING is pinned where it is authored, by
	// TestCredentialNamespace_RefusalNamesTheVariableAndTheShape in the auth
	// package. Here the sentinel is the whole assertion: the error reaches
	// startup unwrapped, so the operator sees that text.
}

// TestSubcommandExit_MalformedNamespaceIsAGenericFailure pins the half of the
// Desktop contract that lives in the dispatcher rather than in the verb: the
// refusal the six *Cmd entry points return exits NON-ZERO and its message is
// printed, so the Desktop sees a failed process with the reason on stderr and
// no JSON state object to parse.
func TestSubcommandExit_MalformedNamespaceIsAGenericFailure(t *testing.T) {
	err := fmt.Errorf("%w: %s=%q", auth.ErrCredentialNamespaceInvalid, auth.CredentialNamespaceEnv, "Dev")

	code, printMessage := subcommandExit(err)
	if code == 0 {
		t.Error("a malformed selector exited 0")
	}
	if code != 1 {
		t.Errorf("subcommandExit = %d, want the generic failure code 1 — a new exit code is a contract change", code)
	}
	if !printMessage {
		t.Error("the refusal message is not printed; the operator would see a bare non-zero exit")
	}
}
