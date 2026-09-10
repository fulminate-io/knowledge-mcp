// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/config"
)

// TestRenderAndWriteConfig_PreservesSelection drives the setup config-write
// path over a temp config that already carries fulminate_account_id. The
// starter template cannot carry the key, so without the preserve-around in
// renderAndWriteConfig a `knowledge setup --reconfigure` would silently erase
// the selection and reroute every later cloud call to the primary account.
func TestRenderAndWriteConfig_PreservesSelection(t *testing.T) {
	const id = "acct_01SETUPPRESERVE0000000000"

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config")

	// Seed a config that carries a selection.
	seed := "[default]\nprovider = \"anthropic\"\nmodel = \"claude-haiku-5\"\n"
	if err := os.WriteFile(cfgPath, []byte(seed), 0o600); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	if err := config.WriteSelectedAccountID(cfgPath, id); err != nil {
		t.Fatalf("seed selection: %v", err)
	}

	// A hard link shares the ORIGINAL inode. The rewrite must publish a new
	// file by rename: the daemon reads this path on a TTL, and a rewrite that
	// truncates in place, or that writes the starter first and patches the
	// selection in afterwards, leaves a selection-free config readable in
	// between. The link still holding the seeded bytes afterwards proves the
	// original inode was never touched.
	seeded, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read seeded config: %v", err)
	}
	shadow := filepath.Join(dir, "shadow")
	if err := os.Link(cfgPath, shadow); err != nil {
		t.Skipf("hard links unsupported here: %v", err)
	}

	detected := config.DetectedProvider{Provider: config.ProviderAnthropic, Model: "claude-haiku-5"}
	if err := renderAndWriteConfig(cfgPath, detected, config.Credentials{}); err != nil {
		t.Fatalf("renderAndWriteConfig: %v", err)
	}

	if after, rerr := os.ReadFile(shadow); rerr != nil {
		t.Fatalf("read shadow: %v", rerr)
	} else if string(after) != string(seeded) {
		t.Errorf("setup rewrote the config inode in place — a concurrent reader could observe a selection-free file.\n shadow now: %q\n seeded:     %q", after, seeded)
	}

	got, err := config.ReadSelectedAccountID(cfgPath)
	if err != nil {
		t.Fatalf("ReadSelectedAccountID after rewrite: %v", err)
	}
	if got != id {
		t.Errorf("selection after setup rewrite = %q, want %q — setup erased the account selection", got, id)
	}

	// The rewrite really did happen (known-positive control): the file now
	// carries starter content, so the preserved id is not just an untouched
	// file that was never rewritten.
	body, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read rewritten config: %v", err)
	}
	if !strings.Contains(string(body), "[summarizer]") {
		t.Errorf("config was not rewritten from the starter; preservation assertion would be vacuous:\n%s", body)
	}

	// Negative control: with no prior selection, setup writes a config that
	// carries none — the preserve step invents nothing.
	freshPath := filepath.Join(t.TempDir(), "config")
	if err := renderAndWriteConfig(freshPath, detected, config.Credentials{}); err != nil {
		t.Fatalf("renderAndWriteConfig(fresh): %v", err)
	}
	fresh, err := config.ReadSelectedAccountID(freshPath)
	if err != nil {
		t.Fatalf("ReadSelectedAccountID(fresh): %v", err)
	}
	if fresh != "" {
		t.Errorf("fresh config carries selection %q, want none", fresh)
	}
}
