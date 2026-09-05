// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"strings"
	"testing"
)

// TestRenderVersionOutput_ServerBinaryLine asserts `knowledge version` carries
// the installed server BINARY's version and its skew line when the stamps
// differ, and stays quiet when they match.
func TestRenderVersionOutput_ServerBinaryLine(t *testing.T) {
	t.Run("differing stamps render the binary line and its skew", func(t *testing.T) {
		out := renderVersionOutput("v0.4.11", "v0.4.11", true, "v0.4.10", true)
		if !strings.Contains(out, "server binary v0.4.10") {
			t.Errorf("the installed server binary's version is missing:\n%s", out)
		}
		if !strings.Contains(out, "binary skew:") {
			t.Errorf("a client and a server binary from different releases must surface a skew line:\n%s", out)
		}
		if !strings.Contains(out, "knowledge install") {
			t.Errorf("the binary-skew remedy must name the re-install:\n%s", out)
		}
		// The EXISTING lines are untouched.
		if !strings.Contains(out, "knowledge v0.4.11\n") || !strings.Contains(out, "server v0.4.11\n") {
			t.Errorf("the existing client and daemon lines were disturbed:\n%s", out)
		}
		// The daemon and the client agree here, so the DAEMON skew line must
		// stay quiet — the two skews are independent.
		if strings.Contains(out, "version skew:") {
			t.Errorf("the daemon-skew line fired on an agreeing daemon:\n%s", out)
		}
	})

	// THE DISCRIMINATING CONTROL: equal stamps stay quiet. Without it, a
	// renderer hard-wired to emit the skew line would pass the row above.
	t.Run("equal stamps render the binary line but NO skew", func(t *testing.T) {
		out := renderVersionOutput("v0.4.10", "v0.4.10", true, "v0.4.10", true)
		if !strings.Contains(out, "server binary v0.4.10") {
			t.Errorf("the binary version line must render even when in sync:\n%s", out)
		}
		if strings.Contains(out, "binary skew:") {
			t.Errorf("matching stamps must not report a skew:\n%s", out)
		}
	})

	t.Run("an unreadable server binary renders neither line", func(t *testing.T) {
		out := renderVersionOutput("v0.4.10", "v0.4.10", true, "", false)
		if strings.Contains(out, "server binary") {
			t.Errorf("an unreadable server binary must render no line at all:\n%s", out)
		}
		if strings.Contains(out, "binary skew:") {
			t.Errorf("an unknown stamp must never read as a mismatch:\n%s", out)
		}
		// The pre-existing output is byte-identical to what it was before this
		// feature existed, which is what makes the addition additive.
		if out != "knowledge v0.4.10\nserver v0.4.10\n" {
			t.Errorf("the unknown-binary render is not byte-identical to the pre-feature output: %q", out)
		}
	})
}

// TestServerBinaryVersion_UnknownWhenUnreadable pins the shared reader's
// degrade: a binary that cannot be located or cannot be read is UNKNOWN, and
// unknown never skews.
func TestServerBinaryVersion_UnknownWhenUnreadable(t *testing.T) {
	// Point the locator at a directory holding no knowledge-server.
	withStubExecutable(t, t.TempDir()+"/knowledge")
	t.Setenv("PATH", t.TempDir())

	ctx := t.Context()
	if v, ok := serverBinaryVersion(ctx); ok {
		t.Errorf("a missing server binary reported a version %q; it must read as unknown", v)
	}
}
