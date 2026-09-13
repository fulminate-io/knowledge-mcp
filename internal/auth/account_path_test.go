// SPDX-License-Identifier: Apache-2.0
package auth

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestConfigureAccountPath(t *testing.T) {
	t.Cleanup(SetSelectedAccountForTest(nil))
	p := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(p, []byte("fulminate_account_id = \"fixture-account\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := ConfigureAccountPath(p); err != nil {
		t.Fatal(err)
	}
	if SelectedAccount().ID(context.Background()) != "fixture-account" {
		t.Fatal("wrong account path")
	}
	if err := ConfigureAccountPath("relative"); err == nil {
		t.Fatal("relative accepted")
	}
}
