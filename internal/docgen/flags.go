// SPDX-License-Identifier: Apache-2.0

package docgen

import (
	"flag"
	"fmt"
	"strings"
)

// renderFlagTable renders fs as a markdown flag table with columns:
// flag / default / usage. It calls fs.VisitAll, which visits flags in lexical
// order, so the rendered output is byte-stable across runs (the CI drift gate
// diffs the regenerated tree).
//
// Defaults come from flag.Flag.DefValue — the value the flag was REGISTERED
// with. Generator callers build the FlagSet via the real register seams
// (registerConfigFlags / registerLifecycleFlags / ...), so const-backed
// defaults (graphclient.DefaultPort, profiling.DefaultPort, ...) resolve to
// their literal values automatically; nothing here hardcodes a port.
//
// Pure function (FlagSet in, string out).
func renderFlagTable(fs *flag.FlagSet) string {
	var b strings.Builder
	b.WriteString("| Flag | Default | Description |\n")
	b.WriteString("| --- | --- | --- |\n")

	any := false
	fs.VisitAll(func(f *flag.Flag) {
		any = true
		def := f.DefValue
		defCell := ""
		if def != "" {
			defCell = "`" + def + "`"
		}
		fmt.Fprintf(&b, "| `--%s` | %s | %s |\n", f.Name, mdCellRaw(defCell), mdCell(f.Usage))
	})
	if !any {
		b.WriteString("| _(no flags)_ | | |\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// mdCellRaw escapes pipes and collapses newlines like mdCell but does NOT trim,
// preserving an already-backticked default cell verbatim.
func mdCellRaw(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "|", "\\|")
	return s
}
