// SPDX-License-Identifier: Apache-2.0

// manage_update_health.go — the manage(status) surface of the daemon's
// background self-updater.
//
// The ticket requires update failures to be LOUD in logs AND surfaced as
// rendered state, never silently retried forever with nothing visible. The log
// half lives with the loop; this is the visible-state half.
//
// It lives in its own file rather than in manage.go because manage.go sits near
// its enforced line ceiling and a render block is exactly the kind of growth
// that pushes a file over it. Only the interface declaration and the two call
// sites touch manage.go.

package tools

import (
	"fmt"
	"strings"
	"time"
)

// UpdateHealth is the exported, immutable snapshot of the background update
// checker's state.
//
// It lives in THIS package, not in the daemon package, because the import graph
// only runs one way: the daemon package imports tools, so tools cannot import
// it back. CollectRunStatus sits here for the same reason. A shared package for
// it is not an option — this repo permits no hand-written shared packages.
//
// WHAT IT CARRIES is driven by what an operator must be able to tell apart:
//   - LastCheck answers "is the updater running at all";
//   - LastInstall answers "has it ever acted";
//   - LatestKnown answers "what did it last see published";
//   - ConsecutiveFailures and LastError answer "is it broken, and how badly";
//   - NoActionReason answers the question a log line alone forces an operator
//     to go read logs for: whether an idle updater is BROKEN or DELIBERATELY
//     not acting here (disabled, a dev build, brew-managed, externally managed,
//     or simply already current).
type UpdateHealth struct {
	LastCheck           time.Time
	LastInstall         time.Time
	LastFailure         time.Time
	LatestKnown         string
	InstalledVersion    string
	ConsecutiveFailures int
	TotalChecks         int
	LastError           string
	NoActionReason      string

	// The ORIGIN fields, set when a check was driven by a cloud
	// client-version refusal rather than by the schedule. They exist so an
	// operator reading the status can see WHY the daemon acted out of band and
	// what the cloud demanded, instead of seeing an unexplained check.
	//
	// TriggerReason is recorded VERBATIM as the cloud sent it, never mapped
	// through a table of known values: an unrecognized reason must reach the
	// operator intact, because that is the only way they learn about a reason
	// shipped after this binary was built.
	TriggerReason        string
	TriggerMinimum       string
	TriggerClientVersion string
	TriggeredAt          time.Time
}

// updateHealther is the optional view of ClientDeps carrying the updater's
// health, with the same structural-typing discipline as the transcript-upload
// overlay: production satisfies it, the existing ClientDeps test fakes do not,
// and the render helpers contribute NOTHING when the type-assert misses.
type updateHealther interface {
	UpdateCheckHealth() (UpdateHealth, bool)
}

// updateCheckHealth reads the snapshot. Returns (zero,false) when deps do not
// satisfy the interface OR when the daemon never reached the loop-spawn stage —
// in either case the render sites emit nothing, so a daemon with no updater
// renders as ABSENT rather than as a healthy zero.
func updateCheckHealth(deps ClientDeps) (UpdateHealth, bool) {
	uh, ok := deps.(updateHealther)
	if !ok {
		return UpdateHealth{}, false
	}
	return uh.UpdateCheckHealth()
}

// updateHealthTS formats a health timestamp as RFC3339 (UTC), or "never" for
// the zero time.
func updateHealthTS(ts time.Time) string {
	if ts.IsZero() {
		return "never"
	}
	return ts.UTC().Format(time.RFC3339)
}

// renderUpdateHealthText renders the operator-facing automatic-update block.
//
// The failure streak and the no-action reason are kept SEPARATELY visible,
// because they answer different questions and collapsing them would hide the
// distinction the snapshot exists to carry. The last error is always shown when
// non-empty: the status can never read healthy with a hidden error.
func renderUpdateHealthText(h UpdateHealth) string {
	var b strings.Builder
	b.WriteString("\n\nAutomatic update:\n")
	fmt.Fprintf(&b, "  Last check: %s\n", updateHealthTS(h.LastCheck))
	fmt.Fprintf(&b, "  Last install: %s\n", updateHealthTS(h.LastInstall))
	fmt.Fprintf(&b, "  Latest known release: %s\n", blankAsUnknown(h.LatestKnown))
	fmt.Fprintf(&b, "  Lifetime: %d check(s)", h.TotalChecks)
	if h.NoActionReason != "" {
		fmt.Fprintf(&b, "\n  no action: %s", h.NoActionReason)
	}
	if h.ConsecutiveFailures > 0 {
		fmt.Fprintf(&b, "\n  systemic: %d consecutive failed check(s) (last failure: %s)",
			h.ConsecutiveFailures, updateHealthTS(h.LastFailure))
	}
	if h.TriggerReason != "" {
		fmt.Fprintf(&b, "\n  last check triggered by a cloud client-version refusal (%s) at %s: the cloud requires %s, this client reports %s",
			h.TriggerReason, updateHealthTS(h.TriggeredAt), blankAsUnknown(h.TriggerMinimum), blankAsUnknown(h.TriggerClientVersion))
	}
	if h.LastError != "" {
		fmt.Fprintf(&b, "\n  last error: %s", h.LastError)
	}
	return b.String()
}

// addUpdateHealthJSON merges the updater's health into the status map so
// format:json carries it too. Timestamps are RFC3339 (or "never"); the last
// error and the no-action reason are the empty string when there is none.
func addUpdateHealthJSON(m map[string]any, h UpdateHealth) {
	m["update_last_check"] = updateHealthTS(h.LastCheck)
	m["update_last_install"] = updateHealthTS(h.LastInstall)
	m["update_last_failure"] = updateHealthTS(h.LastFailure)
	m["update_latest_known"] = h.LatestKnown
	m["update_installed_version"] = h.InstalledVersion
	m["update_consecutive_failures"] = h.ConsecutiveFailures
	m["update_total_checks"] = h.TotalChecks
	m["update_last_error"] = h.LastError
	m["update_no_action_reason"] = h.NoActionReason
	if h.TriggerReason != "" {
		m["update_trigger_reason"] = h.TriggerReason
		m["update_trigger_minimum"] = h.TriggerMinimum
		m["update_trigger_client_version"] = h.TriggerClientVersion
		m["update_triggered_at"] = updateHealthTS(h.TriggeredAt)
	}
}
