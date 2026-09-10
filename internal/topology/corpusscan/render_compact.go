// SPDX-License-Identifier: Apache-2.0

// render_compact.go — the one-line-per-hit render.
//
// WHY IT IS A DIFFERENT SERIALIZATION RATHER THAN A FILTERED ONE. The full body
// is foundation.RenderFindings, indented JSON over every field — and a site
// finding's Summary is the check's ENTIRE prose description, repeated on every
// one of its sites, with each capture's full source text in Evidence beside it.
// Dropping fields from that JSON would still cost a reader an object per hit for
// four values they can read on one line. So this emits lines.
//
// LEAD FINDINGS ARE NEVER COMPACTED. The refusals, the disclosures and the
// truncation notices are what make a bounded result honest — they are emitted
// AHEAD of the match findings precisely so a small top_k cannot clip them — and
// a one-line form of "this run could not execute four checks" is not an honest
// summary of it. They render in full above the compact block, which is also why
// the compact body's size is lead bytes PLUS one bounded line per site rather
// than a line count times a width.

package corpusscan

import (
	"strings"
	"unicode/utf8"

	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

// RenderCompact renders the lead findings in full, then one line per flagged
// site. It is the render a caller scanning its own diff reads.
func RenderCompact(findings []foundation.Finding) (string, error) {
	lead := make([]foundation.Finding, 0, len(findings))
	var sites []foundation.Finding
	for _, f := range findings {
		if IsLeadFinding(f) {
			lead = append(lead, f)
			continue
		}
		sites = append(sites, f)
	}
	body, err := foundation.RenderFindings(lead)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString(body)
	b.WriteString("\n")
	for _, f := range sites {
		b.WriteString(CompactLine(f))
		b.WriteString("\n")
	}
	return b.String(), nil
}

// CompactLine renders ONE site finding as its row, with no trailing newline.
//
// THERE ARE TWO ROW CLASSES because there are two kinds of site, and the
// difference is not cosmetic: an ast site is a file AND A LINE, while a graph
// site is a code-graph node, which yields a file and NEVER a line. Both classes
// carry the same four columns in the same order and are separated by tabs, so a
// consumer reads the severity and the check id without knowing which produced
// the row, and tells the classes apart by the second column: an ast row's
// carries a colon and a decimal line, a graph row's is a bare path.
//
//	ast:   <severity>\t<file>:<line>\t<check id>\t<display name, capped>
//	graph: <severity>\t<file>\t<check id>\t<node id, never truncated>
//
// A ZERO LINE IS NEVER EMITTED AND A NODE ID IS NEVER PRINTED IN THE LINE
// POSITION. The graph finding OMITS its line key entirely because an absent key
// is honest where a zero is a false row, and rendering its Evidence[0] — a node
// id — in the column documented as file:line would undo that one layer up. The
// natural instinct is to fill the gap; do not.
//
// THE NODE ID IS NOT CAPPED, unlike the display name. A truncated id does not
// resolve, and the id is a graph site's whole identity the way file:line is an
// ast site's; the display name, by contrast, is recoverable from the check id,
// so capping it costs nothing and is what bounds the ast row by construction.
func CompactLine(f foundation.Finding) string {
	id := f.Metadata[MetaKeyCheckID]
	if line, ok := f.Metadata[MetaKeyLine]; ok {
		return strings.Join([]string{
			string(f.Severity),
			compactSite(f, line),
			id,
			capDisplayName(displayName(f)),
		}, "\t")
	}
	return strings.Join([]string{
		string(f.Severity),
		f.Metadata[MetaKeyFile],
		id,
		firstEvidence(f),
	}, "\t")
}

// IsLeadFinding reports whether f is one of this analyzer's own lead findings
// rather than a flagged site.
//
// IT IS THE SAME TAXONOMY ClassifyRun FOLDS BY, and the two agreeing is a
// property a test walks every locked title to assert: a title one of them
// recognizes and the other does not would either compact a disclosure or count
// a site line as a flagged site, in opposite directions.
func IsLeadFinding(f foundation.Finding) bool {
	switch {
	case strings.HasPrefix(f.Title, RefusalPrefixUnvalidated),
		strings.HasPrefix(f.Title, RefusalPrefixEnvironment):
		return true
	case strings.HasPrefix(f.Title, TruncationPrefixCheck), f.Title == TruncationTitleRun:
		return true
	case f.Title == DisclosureTitleLLMOnly, f.Title == DisclosureTitleTestFiles,
		f.Title == DisclosureTitleOtherLanguage,
		strings.HasPrefix(f.Title, DisclosurePrefixGraphNotRun),
		strings.HasPrefix(f.Title, DisclosurePrefixOutOfScope),
		strings.HasPrefix(f.Title, DisclosurePrefixScopeApplied):
		return true
	}
	return false
}

// CompactRenderDefault resolves the render form: what the caller asked for, or
// compact when they named a file list and full otherwise.
//
// THE DEFAULT IS THE WHOLE POINT OF THE FILE-LIST SCOPE. A caller scanning its
// own diff wants the hits, not a check's prose description repeated per site; a
// caller scanning a subtree is usually reading the findings themselves. Every
// face resolves it here rather than each spelling the rule, so the tool and the
// CLI cannot default differently for the same call.
func CompactRenderDefault(explicit *bool, files []string) bool {
	if explicit != nil {
		return *explicit
	}
	return len(files) > 0
}

// compactSite renders an ast row's second column, preferring the dedup key the
// finding already carries.
//
// Evidence[0] IS THE ANSWER when it is there: astSiteFinding builds it from the
// match's FILESYSTEM-TRUE path and start line, so it is the same string the
// finding deduplicates on. The metadata pair is the fallback for a finding built
// by any other producer, and it is composed rather than assumed identical.
func compactSite(f foundation.Finding, line string) string {
	if site := firstEvidence(f); site != "" {
		return site
	}
	return f.Metadata[MetaKeyFile] + ":" + line
}

// firstEvidence returns Evidence[0], or the empty string when there is none.
func firstEvidence(f foundation.Finding) string {
	if len(f.Evidence) == 0 {
		return ""
	}
	return f.Evidence[0]
}

// displayName recovers the check's display name from the finding's title.
//
// IT IS DERIVED RATHER THAN READ FROM A NEW METADATA KEY, deliberately: the
// check vocabulary carries no check-name key and never will — the display name
// is the source node's SymbolName — so adding one for this render would be a
// second answer to a question the title already answers. Title is built as
// `<name> at <site>` by exactly one producer, so trimming the site the finding
// carries recovers the name without parsing.
func displayName(f foundation.Finding) string {
	if site := firstEvidence(f); site != "" {
		if name, ok := strings.CutSuffix(f.Title, " at "+site); ok {
			return name
		}
	}
	return f.Title
}

// capDisplayName bounds the display-name column in BYTES, cut on a rune
// boundary, with an ellipsis so a truncation is visible rather than silent. The
// byte bound rather than a rune count is what makes the row's WIDTH a bound: a
// 60-rune name can be 240 bytes.
func capDisplayName(s string) string {
	if len(s) <= CompactNameCap {
		return s
	}
	const ellipsis = "..."
	cut := CompactNameCap - len(ellipsis)
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + ellipsis
}

// compactLineCount is the number of site lines a compact render emits, exposed
// for the render-bound assertion so a test states the block's bound as the row
// class's width times the compacted count rather than guessing at it.
func compactLineCount(findings []foundation.Finding) int {
	n := 0
	for _, f := range findings {
		if !IsLeadFinding(f) {
			n++
		}
	}
	return n
}

// compactRowBound reports the documented per-line bound for f's row class, so a
// caller measuring a block does not have to re-derive which class a row is.
func compactRowBound(f foundation.Finding) int {
	if _, ok := f.Metadata[MetaKeyLine]; ok {
		return CompactRowBoundAst
	}
	return CompactRowBoundGraph
}

// compactLineIsWithinBound reports whether one rendered row is at or under its
// class's documented bound. It exists so the bound is computed in one place
// rather than in each assertion that reads it.
func compactLineIsWithinBound(f foundation.Finding) bool {
	return len(CompactLine(f)) <= compactRowBound(f)
}
