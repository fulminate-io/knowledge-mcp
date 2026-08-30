// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// manage_status_startup_balance_test.go pins the boot-verdict section: it renders
// what the boot pass recorded, and renders NOTHING when the pass has not run.

// startupBalanceDeps is coverageDeps plus the optional boot-verdict capability, so
// the type assertion in renderStartupBalance has something to find.
type startupBalanceDeps struct {
	coverageDeps
	verdicts map[string]string
	ran      bool
}

func (d *startupBalanceDeps) StartupBalanceVerdicts() (map[string]string, bool) {
	return d.verdicts, d.ran
}

func TestRenderStartupBalance(t *testing.T) {
	t.Run("recorded_verdicts_render_verbatim_and_sorted", func(t *testing.T) {
		out := renderStartupBalance(&startupBalanceDeps{
			ran: true,
			verdicts: map[string]string{
				"knowledge/default": "deficit (resident 4 < owed 9)",
				"code/myrepo":       "balanced (resident 781 == owed 781)",
			},
		})
		require.Contains(t, out, "segment balance at startup")
		require.Contains(t, out, "`knowledge/default` — deficit (resident 4 < owed 9)",
			"the recorded verdict renders verbatim — it is the evidence, not a summary of it")
		require.Contains(t, out, "`code/myrepo` — balanced (resident 781 == owed 781)")
		require.Less(t, strings.Index(out, "code/myrepo"), strings.Index(out, "knowledge/default"),
			"graphs render in a stable sorted order, so two status calls on one daemon "+
				"produce comparable output")
		require.Contains(t, out, "SNAPSHOT OF BOOT",
			"the section must say it is a boot snapshot; a reader taking it for a live "+
				"reading would chase a graph that has since been repaired")
	})

	t.Run("an_unrun_pass_renders_nothing", func(t *testing.T) {
		// THE FAILURE THIS FORBIDS is a section that reports health for a check that
		// never happened. Before the boot delay elapses there is no verdict, and
		// silence is the only honest output.
		require.Empty(t, renderStartupBalance(&startupBalanceDeps{ran: false}),
			"a pass that has not run renders no section at all")
	})

	t.Run("deps_without_the_capability_render_nothing", func(t *testing.T) {
		require.Empty(t, renderStartupBalance(&coverageDeps{}),
			"a deps that cannot answer must not grow an empty section")
	})
}
