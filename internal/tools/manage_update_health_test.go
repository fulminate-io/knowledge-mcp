// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// updateHealthDeps embeds the version-capable deps fake and adds the update
// health accessor, so the render path is driven exactly as production reaches
// it — through the optional interface, not by calling the renderer directly.
type updateHealthDeps struct {
	*versionDeps
	health UpdateHealth
	ok     bool
}

func (d *updateHealthDeps) UpdateCheckHealth() (UpdateHealth, bool) { return d.health, d.ok }

// TestManageStatus_RendersAutomaticUpdateHealth drives manage(status) against a
// deps fake carrying a FAILING snapshot and asserts the text names the error
// and the streak, that the JSON carries the matching fields, and — the degrade
// control — that a deps value NOT satisfying the interface renders no update
// block at all and does not error.
func TestManageStatus_RendersAutomaticUpdateHealth(t *testing.T) {
	at := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	base := func() *versionDeps {
		return &versionDeps{
			cloudStatusDeps: &cloudStatusDeps{loggedIn: false},
			live:            fakeLiveness{status: runningStatusMap()},
			clientVer:       "1.0.0", daemonVer: "1.0.0", daemonKnown: true,
		}
	}

	t.Run("a persistently failing updater is visible in text and JSON", func(t *testing.T) {
		deps := &updateHealthDeps{
			versionDeps: base(),
			ok:          true,
			health: UpdateHealth{
				LastCheck:           at,
				LastFailure:         at,
				LatestKnown:         "v2.0.0",
				ConsecutiveFailures: 4,
				TotalChecks:         9,
				LastError:           "release endpoint unreachable",
			},
		}

		body := textBodyTools(handleServerStatus(opCtx(), deps, ""))
		assert.Contains(t, body, "Automatic update:")
		assert.Contains(t, body, "release endpoint unreachable",
			"a failing updater must name its error in the rendered status, not only in a log")
		assert.Contains(t, body, "4 consecutive failed check(s)",
			"the streak is what tells an operator this is persistent rather than a blip")
		assert.Contains(t, body, "v2.0.0")

		var got map[string]any
		require.NoError(t, json.Unmarshal([]byte(textBodyTools(handleServerStatus(opCtx(), deps, "json"))), &got))
		assert.Equal(t, "release endpoint unreachable", got["update_last_error"])
		assert.InDelta(t, 4, got["update_consecutive_failures"], 0.001)
		assert.Equal(t, "v2.0.0", got["update_latest_known"])
		assert.Equal(t, at.Format(time.RFC3339), got["update_last_check"])
	})

	t.Run("a deliberately idle updater names its reason rather than reading broken", func(t *testing.T) {
		deps := &updateHealthDeps{
			versionDeps: base(),
			ok:          true,
			health: UpdateHealth{
				LastCheck:      at,
				TotalChecks:    3,
				NoActionReason: "brew-managed install",
			},
		}
		body := textBodyTools(handleServerStatus(opCtx(), deps, ""))
		assert.Contains(t, body, "no action: brew-managed install",
			"an idle updater must say WHY it is idle; otherwise an operator cannot tell deliberate from broken")
		assert.NotContains(t, body, "systemic:",
			"a refusal is not a failure and must not render as a streak")

		var got map[string]any
		require.NoError(t, json.Unmarshal([]byte(textBodyTools(handleServerStatus(opCtx(), deps, "json"))), &got))
		assert.Equal(t, "brew-managed install", got["update_no_action_reason"])
	})

	// THE DEGRADE CONTROL. Without it, an implementation that unconditionally
	// emitted a healthy zero block would pass every leg above.
	t.Run("deps not satisfying the interface render NO update block and do not error", func(t *testing.T) {
		deps := base()
		res := handleServerStatus(opCtx(), deps, "")
		require.False(t, res.IsError, textBodyTools(res))
		body := textBodyTools(res)
		assert.NotContains(t, body, "Automatic update:",
			"a deps value carrying no updater must render nothing, not a healthy zero block")

		var got map[string]any
		require.NoError(t, json.Unmarshal([]byte(textBodyTools(handleServerStatus(opCtx(), deps, "json"))), &got))
		assert.NotContains(t, got, "update_last_check")
		assert.NotContains(t, got, "update_consecutive_failures")
	})

	// The SAME control one level down: a deps value that DOES satisfy the
	// interface but whose tracker is absent (a daemon whose disable gate
	// refused, so the loops never spawned) also renders nothing.
	t.Run("an absent tracker renders NO update block", func(t *testing.T) {
		deps := &updateHealthDeps{versionDeps: base(), ok: false}
		body := textBodyTools(handleServerStatus(opCtx(), deps, ""))
		assert.NotContains(t, body, "Automatic update:",
			"a daemon that runs no updater must render as absent, not as a healthy zero")
	})
}
