// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/fulminate-io/knowledge-mcp/internal/auth"
)

// withFailingStore points the store seam at a constructor that fails, standing
// in for "any other store error" — the signed-out machine whose existing JSON
// contract must not move. It returns a counter of how many times the seam was
// reached, which is how the ordering row tells "refused before perform" from
// "ran perform and then failed".
func withFailingStore(t *testing.T) *int {
	t.Helper()
	calls := 0
	orig := newStoreFn
	newStoreFn = func() (auth.Store, error) {
		calls++
		return nil, errors.New("credential store unavailable (test stand-in)")
	}
	t.Cleanup(func() { newStoreFn = orig })
	return &calls
}

// withStdin replaces os.Stdin (and the stdinReader seam) with a pipe carrying
// body, for the two verbs that read their request from standard input.
func withStdin(t *testing.T, body string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	if _, err := w.WriteString(body); err != nil {
		t.Fatalf("write stdin fixture: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close stdin fixture: %v", err)
	}
	origStdin, origReader := os.Stdin, stdinReader
	os.Stdin, stdinReader = r, r
	t.Cleanup(func() {
		os.Stdin, stdinReader = origStdin, origReader
		_ = r.Close()
	})
}

// authorizedKeyFixture returns a real ed25519 authorized key, so the terminal
// verb's request reaches its credential read rather than being refused as
// malformed before it.
func authorizedKeyFixture(t *testing.T) string {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate ed25519 key: %v", err)
	}
	signer, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatalf("ssh public key: %v", err)
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer)))
}

// desktopVerb is one of the six Desktop JSON entry points, with argv (and
// stdin) that reaches its perform/run call on a machine whose store cannot be
// opened, and the result code that machine gets today.
type desktopVerb struct {
	name     string
	wantCode string
	drive    func(t *testing.T) error
}

func desktopVerbs(t *testing.T) []desktopVerb {
	t.Helper()
	configPath := filepath.Join(t.TempDir(), "config")
	key := authorizedKeyFixture(t)
	return []desktopVerb{
		{
			name:     "desktop-auth",
			wantCode: "credential_unavailable",
			drive:    func(*testing.T) error { return DesktopAuthCmd([]string{"status", "--config-file", configPath}) },
		},
		{
			name:     "desktop-dashboard",
			wantCode: "sign_in_required",
			drive: func(*testing.T) error {
				return desktopDashboardCmd([]string{`{"operation":"list","account":"account-A"}`})
			},
		},
		{
			name:     "desktop-environment-write",
			wantCode: "sign_in_required",
			drive: func(*testing.T) error {
				return desktopEnvironmentCmd([]string{`{"operation":"delete","account":"account-A","environment":"env-1"}`})
			},
		},
		{
			name:     "desktop-platform",
			wantCode: "unauthenticated",
			drive: func(t *testing.T) error {
				withStdin(t, `{"method":"GET","path":"/auth/me"}`)
				return DesktopPlatformCmd([]string{"request", "--config-file", configPath})
			},
		},
		{
			name:     "desktop-remote",
			wantCode: "sign_in_required",
			drive:    func(*testing.T) error { return DesktopRemoteCmd([]string{"list", "--account", "account-A"}) },
		},
		{
			name:     "desktop-terminal",
			wantCode: "sign_in_required",
			drive: func(t *testing.T) error {
				withStdin(t, `{"account":"account-A","environment":"env-native","ssh_public_key":"`+key+`"}`)
				return desktopTerminalCmd(nil)
			},
		},
	}
}

// TestDesktopVerbs_MalformedNamespaceIsAHardError is the malformed-selector
// half of the six-verb matrix: a hard error carrying the sentinel, no JSON
// state object on stdout, and the perform/run call never entered — so no new
// result code enters the CLI to Desktop contract and nothing a renderer would
// parse is produced.
func TestDesktopVerbs_MalformedNamespaceIsAHardError(t *testing.T) {
	for _, verb := range desktopVerbs(t) {
		t.Run(verb.name, func(t *testing.T) {
			t.Setenv(auth.CredentialNamespaceEnv, "Dev")
			calls := withFailingStore(t)

			out, err := captureStdout(t, func() error { return verb.drive(t) })
			if !errors.Is(err, auth.ErrCredentialNamespaceInvalid) {
				t.Fatalf("%s with a malformed selector returned %v, want ErrCredentialNamespaceInvalid", verb.name, err)
			}
			// The error is returned UNWRAPPED, so the text the dispatcher
			// prints on stderr is the refusal auth authored. Its wording — the
			// variable, the offending value and the accepted shape — is pinned
			// once, where it is produced, by
			// TestCredentialNamespace_RefusalNamesTheVariableAndTheShape;
			// asserting it again here would be a second copy of one contract,
			// and reading a condition out of message text is what the sentinel
			// above exists to avoid.
			if strings.TrimSpace(out) != "" {
				t.Errorf("%s printed %q on stdout, want no JSON state object", verb.name, out)
			}
			if *calls != 0 {
				t.Errorf("%s opened the credential store %d times — the refusal must come before the perform call", verb.name, *calls)
			}
		})
	}
}

// TestDesktopVerbs_OtherStoreErrorsAreUnchanged is the regression guard on the
// other side of the same matrix: every store error that is NOT a malformed
// selector keeps today's JSON result and exit-0 shape. A verb that hard-errored
// on every store error would have broken the signed-out experience, and this
// row is what catches it.
func TestDesktopVerbs_OtherStoreErrorsAreUnchanged(t *testing.T) {
	for _, verb := range desktopVerbs(t) {
		t.Run(verb.name, func(t *testing.T) {
			if err := os.Unsetenv(auth.CredentialNamespaceEnv); err != nil {
				t.Fatalf("unset %s: %v", auth.CredentialNamespaceEnv, err)
			}
			calls := withFailingStore(t)

			out, err := captureStdout(t, func() error { return verb.drive(t) })
			if err != nil {
				t.Fatalf("%s with a plain store error returned %v, want the JSON result and no error", verb.name, err)
			}
			if *calls == 0 {
				t.Fatalf("%s never reached the credential store — the fixture does not exercise the store-error arm", verb.name)
			}
			var got struct {
				Code string `json:"code"`
			}
			if err := json.Unmarshal([]byte(out), &got); err != nil {
				t.Fatalf("%s printed %q, which is not the JSON result object: %v", verb.name, out, err)
			}
			if got.Code != verb.wantCode {
				t.Errorf("%s reported code %q, want the unchanged %q", verb.name, got.Code, verb.wantCode)
			}
		})
	}
}

// TestDesktopVerbs_ValidNamespaceIsNotRefused is the control without which the
// refusal rows prove nothing: the same six verbs under a WELL-FORMED selector
// behave exactly as they do with none, so the refusal is discriminating on the
// value rather than on the variable being present at all.
func TestDesktopVerbs_ValidNamespaceIsNotRefused(t *testing.T) {
	for _, verb := range desktopVerbs(t) {
		t.Run(verb.name, func(t *testing.T) {
			t.Setenv(auth.CredentialNamespaceEnv, "dev")
			calls := withFailingStore(t)

			out, err := captureStdout(t, func() error { return verb.drive(t) })
			if err != nil {
				t.Fatalf("%s under a valid selector returned %v, want the JSON result and no error", verb.name, err)
			}
			if *calls == 0 {
				t.Fatalf("%s never reached the credential store under a valid selector", verb.name)
			}
			if !strings.Contains(out, verb.wantCode) {
				t.Errorf("%s printed %q, want the unchanged %q result", verb.name, out, verb.wantCode)
			}
		})
	}
}
