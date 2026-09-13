// SPDX-License-Identifier: Apache-2.0
package cli

import (
	"os"
	"testing"
)

// TestDesktopSettingsProcess invokes the real command with only the explicit
// scratch configuration selected by the Electron fixture; no auth/store setup.
func TestDesktopSettingsProcess(t *testing.T) {
	if os.Getenv("DESKTOP_SETTINGS_FIXTURE") == "" {
		t.Skip("Electron settings fixture absent")
	}
	var args []string
	for i, arg := range os.Args {
		if arg == "--" {
			args = os.Args[i+1:]
			break
		}
	}
	if len(args) < 2 || args[0] != "desktop-settings" {
		os.Exit(1)
	}
	if DesktopSettingsCmd(args[1:]) != nil {
		os.Exit(1)
	}
	os.Exit(0)
}
