// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// setBootstrapHome selects a scratch resolver without changing process HOME.
func setBootstrapHome(t *testing.T, dir string) {
	t.Helper()
	previous := bootstrapHomeDir
	bootstrapHomeDir = func() (string, error) {
		if dir == "" {
			return "", fmt.Errorf("test home unavailable")
		}
		return dir, nil
	}
	t.Cleanup(func() { bootstrapHomeDir = previous })
}

func TestBootstrapHomeIsolation(t *testing.T) {
	actual, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	scratch, err := bootstrapHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if actual == scratch {
		t.Fatal("suite must inject scratch paths without replacing process home")
	}
	selected := t.TempDir()
	setBootstrapHome(t, selected)
	if got := doctorConfigPath(""); got != filepath.Join(selected, ".knowledge", "config") {
		t.Fatalf("default config = %q", got)
	}
	explicit := filepath.Join(t.TempDir(), "chosen")
	if got := doctorConfigPath(explicit); got != explicit {
		t.Fatalf("explicit config = %q", got)
	}
	after, err := os.UserHomeDir()
	if err != nil || after != actual {
		t.Fatalf("process home changed: %v", err)
	}
}
