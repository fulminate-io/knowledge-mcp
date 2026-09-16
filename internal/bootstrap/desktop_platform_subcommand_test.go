// SPDX-License-Identifier: Apache-2.0

// desktop_platform_subcommand_test.go — the dispatch leg for the Desktop
// platform proxy. A command the table does not carry is UNRECOGNIZED, which is
// indistinguishable from a typo at the call site, so the dispatch entry is
// pinned separately from the command's own behaviour.
//
// The comparisons here are written in the test rather than through an
// assertion library, which is the Google style rule this repository's neighbors
// predate (gostyle-decisions-assert, gostyle-decisions-use-package-testing) and
// which new code follows.

package bootstrap

import (
	"errors"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/cli"
)

// TestDispatchSubcommand_DesktopPlatform pins that the table recognizes the
// proxy subcommand and routes it, by observing the command's OWN argument
// refusal rather than a generic error: an unrecognized subcommand returns
// recognized=false with a nil error, so the pair distinguishes "routed and
// refused the arguments" from "never reached the command".
func TestDispatchSubcommand_DesktopPlatform(t *testing.T) {
	routed := []struct {
		name string
		args []string
		want error
	}{
		{name: "action enum", args: []string{"perform"}, want: cli.ErrPlatformAction},
		{name: "relative config path", args: []string{"request", "--config-file", "relative"}, want: cli.ErrPlatformArguments},
	}
	for _, c := range routed {
		recognized, err := dispatchSubcommand("desktop-platform", c.args)
		if !recognized {
			t.Errorf("%s: desktop-platform must be recognized by the dispatch table", c.name)
			continue
		}
		if !errors.Is(err, c.want) {
			t.Errorf("%s: dispatchSubcommand(desktop-platform, %v) error = %v, want %v — the routed command is what must refuse", c.name, c.args, err, c.want)
		}
	}
	if recognized, _ := dispatchSubcommand("desktop-platformm", []string{"request"}); recognized {
		t.Error("a near-miss subcommand name must stay unrecognized")
	}
}
