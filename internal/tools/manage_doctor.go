// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"fmt"
	"strings"
)

// DoctorCheck is ONE diagnostic result surfaced in the manage(status) doctor
// block — the structured, machine-readable form of the `knowledge doctor` CLI's
// per-check output. The Daemon Status web page's Doctor card renders these:
// failing/warning checks prominent at the top, passing checks collapsible.
//
// Status is a stable string enum — "pass" | "warn" | "fail" | "info" — NOT the
// internal bootstrap checkStatus int (that stays unexported in bootstrap). The
// bootstrap client maps its checkResult into this shape (doctor_status.go), so
// the wire contract lives here in tools (which owns the MCP tool JSON), not in
// the bootstrap check funcs. Detail is the one-line problem description;
// Remediation is the optional "how to fix" hint (empty when there's nothing to
// do). OSS artifact hygiene: these strings carry only local-install diagnostics
// (config/cli_bin/assets/login/staleness) — no private/cloud-backend internals.
type DoctorCheck struct {
	Name        string `json:"name"`
	Status      string `json:"status"`
	Detail      string `json:"detail"`
	Remediation string `json:"remediation"`
}

// doctorChecker is the OPTIONAL view of ClientDeps that manage(status) uses to
// overlay the daemon's diagnostic checks onto the status body. Declared here
// (not on ClientDeps) with the SAME structural-typing discipline as
// pipelineMetricser / transcriptUploadHealther: the production *client satisfies
// it (bootstrap/doctor_status.go), test fakes don't, and handleServerStatus
// degrades to omitting the doctor block when the type-assert misses.
type doctorChecker interface {
	DoctorChecks(ctx context.Context) []DoctorCheck
}

// doctorChecks reads the daemon's diagnostic checks via the optional
// doctorChecker seam. Returns (checks, true) only when deps satisfy the
// interface; (nil, false) for test fakes / degraded modes so the caller omits
// the "doctor" key entirely (the additive degrade contract).
func doctorChecks(ctx context.Context, deps ClientDeps) ([]DoctorCheck, bool) {
	dc, ok := deps.(doctorChecker)
	if !ok {
		return nil, false
	}
	return dc.DoctorChecks(ctx), true
}

// doctorGlyph maps a DoctorCheck.Status to the same terminal-agnostic unicode
// glyph the `knowledge doctor` CLI uses (bootstrap/doctor.go glyphFor), so the
// compact doctor block appended to the TEXT manage(status) render reads
// consistently with the standalone doctor command. No ANSI color codes.
func doctorGlyph(status string) string {
	switch status {
	case "pass":
		return "✓"
	case "warn":
		return "⚠"
	case "fail":
		return "✗"
	default:
		return "ⓘ"
	}
}

// renderDoctorText renders the compact "what's broken" doctor block appended to
// the human-readable manage(status) body, so CLI `manage status` gains the same
// triage the web Doctor card surfaces. Empty checks render nothing. Each check
// is one glyph + name + detail line, with an indented remediation line when set.
func renderDoctorText(checks []DoctorCheck) string {
	if len(checks) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\nDoctor (what's broken):\n")
	for _, c := range checks {
		fmt.Fprintf(&b, "  %s %-16s %s\n", doctorGlyph(c.Status), c.Name, c.Detail)
		if c.Remediation != "" {
			fmt.Fprintf(&b, "      %s\n", c.Remediation)
		}
	}
	return b.String()
}
