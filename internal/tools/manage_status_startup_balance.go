// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"fmt"
	"sort"
	"strings"
)

// manage_status_startup_balance.go renders the BOOT-TIME segment balance verdicts
// beside the live coverage table.
//
// IT IS A DIFFERENT QUESTION FROM THE TABLE ABOVE IT, which is why it is a section
// and not a column. The table reports what each graph looks like NOW; this reports
// what it looked like when the daemon started, before any arm had a chance to act. A
// pool that was pathological at boot and has since been repaired is invisible in the
// live view and is exactly what an operator investigating a restart wants to see.

// startupBalanceReader is the OPTIONAL deps capability the boot verdicts come
// through, type-asserted for the reason loadLiveResidentReader is: only the real
// client can answer it, and putting it on a shared interface would force every fake
// to grow a method with nothing behind it.
//
// THE SECOND RETURN REPORTS WHETHER THE BOOT PASS RAN. It is not derivable from the
// map: a pass that ran and found no segment-bearing graphs leaves the same empty map
// a pass that never ran leaves, and reporting a clean boot for a check that never
// happened is the collapse this whole surface exists to refuse.
type startupBalanceReader interface {
	StartupBalanceVerdicts() (map[string]string, bool)
}

// renderStartupBalance renders the boot verdict section, or "" when this deps cannot
// answer.
//
// AN UNRUN PASS RENDERS NOTHING RATHER THAN A REASSURING LINE. Before the boot delay
// elapses there is no verdict to report, and a section saying so on every early
// status call would be noise; a section claiming health would be a lie. The verdicts
// appear once they exist.
func renderStartupBalance(deps ClientDeps) string {
	r, isReader := deps.(startupBalanceReader)
	if !isReader {
		return ""
	}
	verdicts, ran := r.StartupBalanceVerdicts()
	if !ran || len(verdicts) == 0 {
		return ""
	}

	labels := make([]string, 0, len(verdicts))
	for label := range verdicts {
		labels = append(labels, label)
	}
	sort.Strings(labels)

	var sb strings.Builder
	sb.WriteString("\n\n### segment balance at startup\n")
	sb.WriteString("_the balance verdict formed ONCE per segment-bearing graph shortly after this daemon started, " +
		"comparing each graph's distinct live-searchable document count against its vector count. It is a " +
		"SNAPSHOT OF BOOT, not a live reading — a graph repaired since then still shows the state it started " +
		"in, which is the point. Nothing was reaped or rebuilt on the strength of it; the reconcile and " +
		"quiescence arms own repair._\n\n")
	for _, label := range labels {
		fmt.Fprintf(&sb, "- `%s` — %s\n", label, verdicts[label])
	}
	return sb.String()
}
