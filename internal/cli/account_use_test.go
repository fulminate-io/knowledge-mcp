// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/auth"
	"github.com/fulminate-io/knowledge-mcp/internal/config"
)

// twoAccountsBody: one subscribed, one not.
const twoAccountsBody = `{"accounts":[
	{"id":"acct_01ACME","name":"Acme Inc","slug":"acme","role":"owner","has_active_subscription":true},
	{"id":"acct_01HOBBY","name":"Hobby Co","slug":"hobby","role":"member","has_active_subscription":false}
],"count":2}`

// useHomeWithConfig points $HOME at a temp dir holding a config file, and
// returns the config path. config.DefaultPath resolves under $HOME, so this
// keeps the test off the developer's real config.
func useHomeWithConfig(t *testing.T, seedSelection string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".knowledge")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, "config")
	body := "# a comment the writer must preserve\n\n[default]\nprovider = \"anthropic\"\nmodel = \"claude-haiku-5\"\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	if seedSelection != "" {
		if err := config.WriteSelectedAccountID(path, seedSelection); err != nil {
			t.Fatalf("seed selection: %v", err)
		}
	}
	// Confirm DefaultPath agrees, so a failure to resolve $HOME cannot make
	// these tests silently assert against the wrong file.
	got, err := config.DefaultPath()
	if err != nil {
		t.Fatalf("DefaultPath: %v", err)
	}
	if got != path {
		t.Fatalf("DefaultPath = %q, want %q", got, path)
	}
	return path
}

func readConfig(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	return string(b)
}

// TestAccountUseCmd_RefusesNonMember pins the membership refusal and that the
// config file is left byte-for-byte unchanged.
func TestAccountUseCmd_RefusesNonMember(t *testing.T) {
	serveAccounts(t, twoAccountsBody)
	path := useHomeWithConfig(t, "")
	before := readConfig(t, path)

	err := AccountUseCmd([]string{"someone-elses-account"})
	if err == nil {
		t.Fatal("want a refusal, got nil")
	}
	for _, want := range []string{
		"you are not a member of any Fulminate account with id or slug",
		"someone-elses-account",
		"knowledge accounts",
		"invite you",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}
	if after := readConfig(t, path); after != before {
		t.Errorf("a refused selection modified the config.\n got: %q\nwant: %q", after, before)
	}
}

// TestAccountUseCmd_RefusesUnsubscribed pins the CEO's "free accounts do not
// get cloud graph access at all" as a hard refusal, with the subscribe remedy.
func TestAccountUseCmd_RefusesUnsubscribed(t *testing.T) {
	serveAccounts(t, twoAccountsBody)
	path := useHomeWithConfig(t, "")
	before := readConfig(t, path)

	err := AccountUseCmd([]string{"hobby"})
	if err == nil {
		t.Fatal("want a refusal for an unsubscribed account, got nil")
	}
	for _, want := range []string{
		"has no active subscription",
		"no cloud graph access at all",
		"subscribe",
		"billing",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}
	if after := readConfig(t, path); after != before {
		t.Errorf("a refused selection modified the config.\n got: %q\nwant: %q", after, before)
	}
}

// TestAccountUseCmd_ResolvesSlugAndPersistsUUID proves the slug argument
// resolves to the account UUID and that the UUID — not the slug — is what
// lands in the config.
func TestAccountUseCmd_ResolvesSlugAndPersistsUUID(t *testing.T) {
	serveAccounts(t, twoAccountsBody)
	path := useHomeWithConfig(t, "")

	out, err := captureStdout(t, func() error { return AccountUseCmd([]string{"acme"}) })
	if err != nil {
		t.Fatalf("AccountUseCmd: %v", err)
	}

	got, err := config.ReadSelectedAccountID(path)
	if err != nil {
		t.Fatalf("ReadSelectedAccountID: %v", err)
	}
	if got != "acct_01ACME" {
		t.Errorf("persisted selection = %q, want the UUID acct_01ACME", got)
	}
	body := readConfig(t, path)
	if strings.Contains(body, `fulminate_account_id = "acme"`) {
		t.Error("the slug was persisted; the id must be")
	}
	if !strings.Contains(body, "# a comment the writer must preserve") {
		t.Error("the write did not preserve the file's comments")
	}
	if !strings.Contains(out, "acct_01ACME") || !strings.Contains(out, "acme") {
		t.Errorf("confirmation %q does not name both the slug and the id", out)
	}

	// An id argument resolves too.
	if err := AccountUseCmd([]string{"acct_01ACME"}); err != nil {
		t.Fatalf("AccountUseCmd(by id): %v", err)
	}
	if got, _ := config.ReadSelectedAccountID(path); got != "acct_01ACME" {
		t.Errorf("after selecting by id: %q, want acct_01ACME", got)
	}
}

// TestAccountUseCmd_UnreachableEndpointWritesNothing proves an unchecked id
// never reaches the config file.
func TestAccountUseCmd_UnreachableEndpointWritesNothing(t *testing.T) {
	// Point the transport at a closed port: nothing is listening.
	prior := buildSyncTransportFn
	buildSyncTransportFn = func() (*auth.Transport, error) {
		return auth.NewSyncTransport("http://127.0.0.1:1", auth.StaticTokenSource{AccessToken: "tok"}), nil
	}
	t.Cleanup(func() { buildSyncTransportFn = prior })

	path := useHomeWithConfig(t, "")
	before := readConfig(t, path)

	err := AccountUseCmd([]string{"acme"})
	if err == nil {
		t.Fatal("an unreachable list endpoint must fail, not write optimistically")
	}
	if !strings.Contains(err.Error(), "nothing was changed") {
		t.Errorf("error %q does not say the config was left alone", err)
	}
	if after := readConfig(t, path); after != before {
		t.Errorf("the config was written despite an unchecked account.\n got: %q\nwant: %q", after, before)
	}
	if got, _ := config.ReadSelectedAccountID(path); got != "" {
		t.Errorf("an unchecked id reached the config: %q", got)
	}
}

// TestAccountUseCmd_CannotUnsetAndDoesNotAskForRestart proves the two
// permanence-and-UX obligations: no flag or argument value clears the entry,
// and the confirmation never tells the user to restart anything (the
// dispatcher restarts the daemon for them).
func TestAccountUseCmd_CannotUnsetAndDoesNotAskForRestart(t *testing.T) {
	serveAccounts(t, twoAccountsBody)
	path := useHomeWithConfig(t, "acct_01ACME")

	// No argument value can clear the selection: an empty argument, a
	// whitespace argument, and the clear-shaped flags all fail with the
	// selection still in place.
	for _, args := range [][]string{
		{""},
		{"   "},
		{"--clear"},
		{"--unset"},
		{"--force", "acme"},
		{},
	} {
		if err := AccountUseCmd(args); err == nil {
			t.Errorf("AccountUseCmd(%q) unexpectedly succeeded", args)
		}
		got, err := config.ReadSelectedAccountID(path)
		if err != nil {
			t.Fatalf("ReadSelectedAccountID: %v", err)
		}
		if got != "acct_01ACME" {
			t.Errorf("after AccountUseCmd(%q) the selection is %q; it must never be cleared", args, got)
		}
	}

	// Known-positive control: a valid argument DOES change the selection, so
	// the never-cleared assertions above are not vacuous.
	out, err := captureStdout(t, func() error { return AccountUseCmd([]string{"acme"}) })
	if err != nil {
		t.Fatalf("AccountUseCmd(acme): %v", err)
	}
	if got, _ := config.ReadSelectedAccountID(path); got != "acct_01ACME" {
		t.Fatalf("control: selection = %q", got)
	}

	lower := strings.ToLower(out)
	for _, banned := range []string{"restart", "reload", "relaunch", "brew services"} {
		if strings.Contains(lower, banned) {
			t.Errorf("confirmation asks the user to %q; the switch is applied for them:\n%s", banned, out)
		}
	}
}
