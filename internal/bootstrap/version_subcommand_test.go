// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"strings"
	"testing"
)

// TestRenderVersionOutput proves the `knowledge version` body: the client line
// always renders, the server line renders only when the daemon probe succeeded,
// and the shared skew line follows only when both stamps are known and differ.
func TestRenderVersionOutput(t *testing.T) {
	t.Run("skew line on differing stamps", func(t *testing.T) {
		out := renderVersionOutput("dev", "v0.4.10", true)
		if !strings.Contains(out, "knowledge dev\n") {
			t.Errorf("missing client line in %q", out)
		}
		if !strings.Contains(out, "server v0.4.10\n") {
			t.Errorf("missing server line in %q", out)
		}
		if !strings.Contains(out, "version skew:") {
			t.Errorf("expected skew line in %q", out)
		}
		if !strings.Contains(out, "dev") || !strings.Contains(out, "v0.4.10") {
			t.Errorf("skew line must carry both versions: %q", out)
		}
	})

	t.Run("no skew line on equal stamps", func(t *testing.T) {
		out := renderVersionOutput("v0.4.10", "v0.4.10", true)
		if !strings.Contains(out, "knowledge v0.4.10\n") {
			t.Errorf("missing client line in %q", out)
		}
		if !strings.Contains(out, "server v0.4.10\n") {
			t.Errorf("missing server line in %q", out)
		}
		if strings.Contains(out, "version skew:") {
			t.Errorf("equal stamps must not emit a skew line: %q", out)
		}
	})

	t.Run("omits server + skew lines when daemon unknown", func(t *testing.T) {
		out := renderVersionOutput("dev", "", false)
		if !strings.Contains(out, "knowledge dev\n") {
			t.Errorf("missing client line in %q", out)
		}
		if strings.Contains(out, "server ") {
			t.Errorf("no daemon known, so no server line: %q", out)
		}
		if strings.Contains(out, "version skew:") {
			t.Errorf("no daemon known, so no skew line: %q", out)
		}
	})
}
