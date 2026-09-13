// SPDX-License-Identifier: Apache-2.0
package bootstrap

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/config"
)

func TestRuntimeExplicitConfig(t *testing.T) {
	for _, headless := range []bool{false, true} {
		t.Run(map[bool]string{false: "normal", true: "headless"}[headless], func(t *testing.T) {
			t.Cleanup(config.SetForTest(nil))
			p := filepath.Join(t.TempDir(), "config")
			if err := os.WriteFile(p, []byte("[credentials]\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := loadExplicitBootConfig(p); err != nil {
				t.Fatal(err)
			}
			if !config.Loaded() {
				t.Fatal("config not loaded")
			}
			if err := loadExplicitBootConfig(p + "missing"); err == nil {
				t.Fatal("missing explicit config accepted")
			}
			if err := os.WriteFile(p, []byte("[broken"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := loadExplicitBootConfig(p); err == nil {
				t.Fatal("malformed explicit config accepted")
			}
			if err := loadExplicitBootConfig("relative"); err == nil {
				t.Fatal("relative config accepted")
			}
		})
	}
}
