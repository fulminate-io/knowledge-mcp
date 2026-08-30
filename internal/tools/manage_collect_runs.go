// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"fmt"
	"strings"
	"time"
)

// manage_collect_runs.go holds the manage(status) collect-run section: the
// optional reporter interface, the snapshot accessor, and the text/json renderers.
// handleServerStatus and handleCloudStatus (manage.go) splice these into both the
// text and json status surfaces. Split out of manage.go to keep that file under the
// file-length cap; pure relocation, no behavior change.

// collectRunReporter is the optional view of ClientDeps that manage(status) uses
// to surface in-flight + last-run collect state. Declared here (not on
// ClientDeps) with the SAME structural-typing discipline as pipelineMetricser /
// transcriptUploadHealther: production *client satisfies it, the existing test
// fakes don't, and the render helpers degrade to nothing when the assert misses.
type collectRunReporter interface {
	CollectRunSnapshot() []CollectRunStatus
}

// collectRunSnapshot reads the per-target collect-run snapshot. Returns
// (runs, true) only when deps satisfy collectRunReporter AND at least one run is
// present; (nil, false) otherwise — both the text section and the json key emit
// nothing in that case, keeping existing manage(status) output byte-identical (the
// SAME degrade contract as transcriptUploadHealth).
func collectRunSnapshot(deps ClientDeps) ([]CollectRunStatus, bool) {
	r, ok := deps.(collectRunReporter)
	if !ok {
		return nil, false
	}
	runs := r.CollectRunSnapshot()
	if len(runs) == 0 {
		return nil, false
	}
	return runs, true
}

// collectRunSection is the text splice point for the collect-run block. Returns ""
// when there is nothing to show (interface absent or empty snapshot) — the SAME
// empty-degrades-to-nothing contract as transcriptBlock at handleServerStatus.
func collectRunSection(deps ClientDeps) string {
	runs, ok := collectRunSnapshot(deps)
	if !ok {
		return ""
	}
	return renderCollectRunsText(runs)
}

// renderCollectRunsText renders the operator-facing collect-run block: one line
// per target — a running target as "<label>: running (<elapsed> elapsed)" (elapsed
// = now - StartedAt, rounded to the second) and a completed/failed target as
// "<label>: <state> (<duration>[, error: <err>][, <composition>])". Mirrors
// renderTranscriptHealthText.
//
// ONE CARRIER PER PATH: the composition rides the COMPLETED line only. A failed
// run's composition is already embedded in the error text the failure carries, so
// appending it here as well would print it twice on the exact failure the whole
// mechanism exists to report. An empty composition degrades to nothing, keeping a
// composition-free run's line byte-identical.
func renderCollectRunsText(runs []CollectRunStatus) string {
	now := time.Now()
	lines := make([]string, 0, len(runs))
	for _, r := range runs {
		switch {
		case r.State == "running":
			lines = append(lines, fmt.Sprintf("  %s: running (%s elapsed)", r.Label, now.Sub(r.StartedAt).Round(time.Second)))
		case r.Err != "":
			lines = append(lines, fmt.Sprintf("  %s: %s (%s, error: %s)", r.Label, r.State, r.FinishedAt.Sub(r.StartedAt).Round(time.Second), r.Err))
		case r.Composition != "":
			lines = append(lines, fmt.Sprintf("  %s: %s (%s, %s)", r.Label, r.State, r.FinishedAt.Sub(r.StartedAt).Round(time.Second), r.Composition))
		default:
			lines = append(lines, fmt.Sprintf("  %s: %s (%s)", r.Label, r.State, r.FinishedAt.Sub(r.StartedAt).Round(time.Second)))
		}
	}
	return "\n\nCollect runs:\n" + strings.Join(lines, "\n")
}

// addCollectRunsJSON merges the collect-run snapshot into the status map under the
// "collect_runs" key so format:json carries it too. Each entry carries target,
// label, and state, plus elapsed_seconds (running) or duration_seconds + error
// (completed/failed), plus composition on a COMPLETED entry. Mirrors
// addTranscriptHealthJSON.
//
// The composition key is present on completed entries only, for the same
// one-carrier-per-path reason renderCollectRunsText carries: a failed run's
// composition already rides its error string.
func addCollectRunsJSON(m map[string]any, runs []CollectRunStatus) {
	now := time.Now()
	entries := make([]map[string]any, 0, len(runs))
	for _, r := range runs {
		e := map[string]any{"target": r.Target, "label": r.Label, "state": r.State}
		if r.State == "running" {
			e["elapsed_seconds"] = now.Sub(r.StartedAt).Round(time.Second).Seconds()
		} else {
			e["duration_seconds"] = r.FinishedAt.Sub(r.StartedAt).Round(time.Second).Seconds()
			if r.Err != "" {
				e["error"] = r.Err
			}
			if r.Err == "" && r.Composition != "" {
				e["composition"] = r.Composition
			}
		}
		entries = append(entries, e)
	}
	m["collect_runs"] = entries
}
