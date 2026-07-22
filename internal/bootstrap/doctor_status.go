// SPDX-License-Identifier: Apache-2.0

// doctor_status.go — the bridge that surfaces the `knowledge doctor` diagnostic
// checks through manage(status). The doctor check funcs live in this package
// (bootstrap imports tools, so tools cannot import bootstrap); rather than move
// them, the *client implements the OPTIONAL tools.doctorChecker seam here and
// converts each internal checkResult into the exported tools.DoctorCheck wire
// shape. This is the SAME optional-interface degrade pattern the pipeline /
// transcript / collect-run overlays use — no new package, no import cycle.

package bootstrap

import (
	"context"

	"github.com/fulminate-io/knowledge-mcp/internal/tools"
)

// DoctorChecks runs the shared non-deep diagnostic checks (defaultChecks — the
// exact set the `knowledge doctor` CLI runs, minus --deep) and converts each
// into a tools.DoctorCheck for the manage(status) doctor block. Satisfies the
// optional tools.doctorChecker interface handleServerStatus type-asserts; a
// *client that omitted this method would simply omit the doctor block (the same
// additive degrade contract as PipelineMetrics / TranscriptUploadHealth).
//
// It passes c.port (the daemon's TCP server port) and an empty configFile so the
// checks resolve the default ~/.knowledge/config path. --deep is deliberately
// NOT run: defaultChecks excludes checkProvidersDeep, so no provider network
// calls happen on a status poll. checkCodeStaleness DOES shell out to git
// (coderun.CommitsBehind), but the only repeating caller of manage(status,json)
// is the web page's 10-minute poll, so this is not a hot path (CEO cadence
// decision) and needs no TTL cache in v1.
func (c *client) DoctorChecks(_ context.Context) []tools.DoctorCheck {
	results := defaultChecks(c.port, "")
	out := make([]tools.DoctorCheck, 0, len(results))
	for _, r := range results {
		out = append(out, tools.DoctorCheck{
			Name:        r.name,
			Status:      doctorStatusLabel(r.status),
			Detail:      r.msg,
			Remediation: r.detail,
		})
	}
	return out
}

// doctorStatusLabel maps the internal checkStatus enum onto the stable
// web-facing string the tools.DoctorCheck.Status field carries. Kept next to
// the only caller so the enum→string contract is one hop from the seam.
func doctorStatusLabel(s checkStatus) string {
	switch s {
	case statusOK:
		return "pass"
	case statusWarn:
		return "warn"
	case statusErr:
		return "fail"
	default: // statusInfo
		return "info"
	}
}
