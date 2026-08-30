// SPDX-License-Identifier: Apache-2.0

// render.go is the READ SURFACE, and it is a first-class symbol rather than
// formatting scattered through a gated test. Every rule about how a calibration
// number is DISPLAYED is this file's contract, and render_test.go asserts its
// output TEXT rather than struct flags.
//
// THE STANDING RULE THIS FILE EXISTS FOR: any rule about how a value is
// PRESENTED needs a symbol that owns the presentation and a test that reads the
// presented text. Gating only the computation still lets a correct report be
// transcribed into three misleading figures; gating only the transcription
// leaves the report itself unverified. Both halves, always.
//
// EVERY QUOTED PHRASE BELOW IS A LOCKED LITERAL written unbroken on a single
// line, because a test asserts it verbatim and golangci-lint's misspell linter
// rewrites Go string literals exactly as it rewrites comments.
package calibration

import (
	"fmt"
	"sort"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

// The locked display literals. Declared once and cited by every rule below.
const (
	// undefinedPrecision is R1's phrase for a precision with no site claims.
	undefinedPrecision = "undefined (no site claims)"
	// undefinedRecall is R1's phrase for a recall with no ground-truth positives.
	undefinedRecall = "undefined (no ground-truth positives at this commit)"
	// joinedZeroDisclosure is R2's phrase, which REPLACES every precision figure.
	joinedZeroDisclosure = "zero join: no scan claim landed on a ground-truth site, so precision is not reportable for this run"
	// mirrorOnlyCounterpart is R4's phrase for an alert with no counterpart here.
	mirrorOnlyCounterpart = "no counterpart in this repo (mirror-only)"
	// refusalPrefix opens R5's refusal ledger.
	refusalPrefix = "refusals: "
	// scopeAstOnly is R7's phrase when ast_pattern is the only executed kind.
	scopeAstOnly = "scope: ast_pattern checks only; no graph, threshold or flow check is executable in this tree"
	// scopeExecutedPrefix opens R7's phrase for any other non-empty kind set.
	scopeExecutedPrefix = "scope: check kinds executed: "
	// scopeUndeclared is R7's phrase when the caller declared no kinds.
	scopeUndeclared = "scope: check kinds not declared by the caller, so the coverage of this report is unknown"
	// blockMarkerPrefix opens R8's delimited block; blockTerminator closes it.
	blockMarkerPrefix = "=== codeql-calibration report: commit "
	blockTerminator   = "=== end codeql-calibration report ==="
)

// severityOrder is foundation's own declared urgency order, least to most.
var severityOrder = []foundation.Severity{
	foundation.SeverityInfo,
	foundation.SeverityNotice,
	foundation.SeverityWarning,
	foundation.SeverityCritical,
}

// RenderReport renders one commit's measurement as a contiguous, delimited,
// commit-identified block. The block is printed into a scan window full of
// benign discovery warnings and is meant to be pasted VERBATIM rather than
// transcribed, which is only an instruction anyone can follow once the block has
// a machine-recognizable extent.
func RenderReport(r ScoreReport) string {
	var b strings.Builder
	// R8: first line.
	fmt.Fprintf(&b, "%s%s ===\n", blockMarkerPrefix, r.CommitSHA)
	// R7: exactly one scope line per block, never omitted.
	fmt.Fprintf(&b, "%s\n", scopeLine(r.CheckKinds))

	refusals := renderRefusals(r.NonSite)
	b.WriteString("\nper-check precision:\n")
	if len(r.Checks) == 0 {
		b.WriteString("  (no check produced a claim)\n")
	}
	for _, c := range r.Checks {
		// R2: when the join is empty the disclosure REPLACES every precision
		// figure, rather than appearing beside one.
		if r.JoinedZero {
			fmt.Fprintf(&b, "  %s: %s [%s]\n", c.CheckID, joinedZeroDisclosure, refusals)
			continue
		}
		// R1 + R3 + R5: undefined renders as a phrase; a defined figure always
		// carries its denominator, and the refusal ledger sits beside it.
		if !c.PrecisionDefined {
			fmt.Fprintf(&b, "  %s: %s [%s]\n", c.CheckID, undefinedPrecision, refusals)
			continue
		}
		fmt.Fprintf(&b, "  %s: %s (%d of %d site claims) [%s]\n",
			c.CheckID, pct(c.Precision), c.SiteMatched, c.SiteClaims, refusals)
	}

	b.WriteString("\nper-rule recall:\n")
	if len(r.Rules) == 0 {
		b.WriteString("  (no ground-truth rule at this commit)\n")
	}
	for _, rs := range r.Rules {
		if !rs.RecallDefined {
			fmt.Fprintf(&b, "  %s: %s\n", rs.RuleID, undefinedRecall)
			continue
		}
		fmt.Fprintf(&b, "  %s: line %s (%d of %d alerts), file %s (%d of %d alerts)\n",
			rs.RuleID,
			pct(rs.Recall), rs.LineHit, rs.GroundTruth,
			pct(rs.FileRecall), rs.FileHit, rs.GroundTruth)
	}

	// R4: every unmatched alert names its counterpart here, or says outright
	// that it has none. An empty field is never acceptable.
	b.WriteString("\nunmatched ground truth (CodeQL flagged it, the scanner did not):\n")
	if len(r.Unmatched) == 0 {
		b.WriteString("  (none)\n")
	}
	for _, a := range r.Unmatched {
		counterpart := a.InternalPath
		if a.InternalClass == PathMirrorOnly || counterpart == "" {
			counterpart = mirrorOnlyCounterpart
		}
		fmt.Fprintf(&b, "  %s:%d [%s] -> %s\n", a.MirrorPath, a.StartLine, a.RuleID, counterpart)
	}

	// R3 + R6: the fingerprint ratio is LINE-granular, and the weaker
	// file-granular over-flags are their own labeled count rather than being
	// folded into it.
	b.WriteString("\nover-flagging:\n")
	fmt.Fprintf(&b, "  extra site claims: %d against %d ground-truth alerts\n", len(r.Extra), groundTruthTotal(r))
	fmt.Fprintf(&b, "  extra file claims: %d (file-granular, counted apart from the site ratio)\n", len(r.ExtraFile))
	fmt.Fprintf(&b, "  %s\n", refusals)

	b.WriteString(blockTerminator + "\n")
	return b.String()
}

// scopeLine renders R7's three states. Silence is not an option in any of them.
func scopeLine(kinds []string) string {
	if len(kinds) == 0 {
		return scopeUndeclared
	}
	if len(kinds) == 1 && kinds[0] == "ast_pattern" {
		return scopeAstOnly
	}
	sorted := append([]string(nil), kinds...)
	sort.Strings(sorted)
	return scopeExecutedPrefix + strings.Join(sorted, ", ")
}

// renderRefusals produces R5: the count, plus a per-severity breakdown in
// foundation's own urgency order when the count is non-zero. A thin scan and a
// mostly-refused one must not read the same.
func renderRefusals(nonSite []foundation.Finding) string {
	if len(nonSite) == 0 {
		return refusalPrefix + "0"
	}
	counts := map[foundation.Severity]int{}
	for _, f := range nonSite {
		counts[f.Severity]++
	}
	parts := make([]string, 0, len(counts))
	for _, s := range severityOrder {
		if n := counts[s]; n > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", s, n))
		}
		delete(counts, s)
	}
	// An empty Severity renders as "unset" rather than as a blank label.
	if n := counts[""]; n > 0 {
		parts = append(parts, fmt.Sprintf("unset %d", n))
	}
	return fmt.Sprintf("%s%d (%s)", refusalPrefix, len(nonSite), strings.Join(parts, ", "))
}

// groundTruthTotal sums the ground truth across rules, for R3's denominator on
// the over-flagging ratio.
func groundTruthTotal(r ScoreReport) int {
	total := 0
	for _, rs := range r.Rules {
		total += rs.GroundTruth
	}
	return total
}

// pct renders a ratio as a percentage. Its denominator is always printed beside
// it by the caller, per R3.
func pct(v float64) string {
	return fmt.Sprintf("%.1f%%", v*100)
}
