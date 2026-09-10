// SPDX-License-Identifier: Apache-2.0

// style_rule_reference.go — a style rule is a REFERENCE in an assembled ticket
// or plan, never a hydrated body.
//
// WHY THIS RENDER EXISTS. Every other language pattern in an assembly carries
// its whole dsl_pattern in an untruncated fence, and that is deliberate for a
// pattern: a pattern body IS the artifact a reviewer reads. A style rule is a
// different animal. Its body is prose, plus a check shape, plus a where-tree,
// plus two fixture bodies, and an assembly that attaches several of them spends
// a lane's whole context on text it did not ask for and mostly will not read.
// So a style rule renders as its id, its severity and a capped summary — enough
// to decide whether the rule bears on the work — and ONE instruction line tells
// the reader to fetch the bodies it wants in a single by-ids read.
//
// ONE INSTRUCTION LINE PER ASSEMBLY, NOT PER RULE, AND THAT IS THE POINT. The
// failure this render exists to prevent is a reader issuing one lookup per
// referenced rule and hitting the tool's response limits partway through. An
// instruction repeated per rule teaches exactly the loop it is trying to stop,
// so the ids are collected across every section that referenced one and named
// together in a single call shape.
//
// THE COLUMNS AND THEIR CAPS ARE THE COMPACT INDEX'S, read from kgtypes. This
// render caps THREE variable-width columns — severity, scope and summary — and
// the index read renders the same rule one layer down as a tab-separated row
// capping those three and a fourth this render does not carry, the sister-check
// id; every one of them is cut to the same width by the same helper. This render
// is that row's markdown sibling. Sharing the vocabulary is what keeps the two
// from drifting into two different notions of "compact".
//
// SCOPE ONLY WHEN THE RULE CARRIES ONE. The index always renders the scope
// column, with `-` for a rule that applies everywhere, because a positional
// tab-separated row cannot omit a column. A markdown line can, and most rules
// are unscoped, so the column would be `-` on most lines and buy nothing.

package render

import (
	"fmt"
	"strconv"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/corpus"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

const (
	// styleRefColumnSeparator joins a reference line's columns. It is a
	// constant rather than an inline literal because the reference line is a
	// shape a reader parses back — the test that lifts the summary column off a
	// rendered line splits on this, so the render and the read agree by
	// construction.
	styleRefColumnSeparator = " — "

	// styleRefAbsentColumn stands in for a column the rule left unset, on the
	// same reasoning styleScopeColumn's `-` carries one layer down: "the rule
	// says nothing here" and "the render dropped a column" have to be different
	// things a reader can tell apart.
	styleRefAbsentColumn = "-"

	// styleRefInstructionMarker opens the instruction line. Callers count
	// occurrences of THIS to assert the line appears once per assembly, so it is
	// named rather than spelled twice.
	styleRefInstructionMarker = "**Style rules are REFERENCES, not hydrated.**"

	// styleRefInstructionBound bounds the instruction line's FIXED prose — every
	// byte of it except the ids. The ids are counted separately below because
	// their width is data rather than a property of this render.
	//
	// FIXED IS ITS TERM CLASS and it is the only one it has: the prose is a
	// literal in this file, so nothing about the corpus can move it. The ids are
	// the render's one input and styleRefBlockBound measures them.
	styleRefInstructionBound = 220

	// styleRefInstructionIDSeparatorBytes is the `, ` this render writes between
	// one id and the next inside the by-ids list. It is the ONLY per-id byte
	// this render emits on its own account; every other byte an id costs is the
	// id's own quoted form, which the bound measures rather than allows for.
	//
	// IT USED TO BE FOUR, and the two bytes it lost are the lesson. The retired
	// figure counted a pair of delimiter bytes alongside the separator — but
	// those delimiters are strconv.Quote's, and pairing them with a flat
	// allowance invited the reader to believe the ESCAPING between them was
	// allowed for too. It was not: a double quote and a backslash render at two
	// bytes and a control byte at four, so the allowance was false above 44
	// bytes of expansion. The delimiters now ride inside the measured term where
	// they belong and this constant is the separator alone.
	//
	// The LAST id in a list spends no separator, so this stays an upper bound
	// rather than an exact figure.
	styleRefInstructionIDSeparatorBytes = 2
)

// isStyleRuleNode reports whether a node attached to a ticket or plan is a style
// rule, by the practice-kind marker the style-rule vocabulary defines.
//
// IT IS A VALUE CHECK ON practice_kind, never a "does this node carry a
// dsl_pattern" heuristic: the 24 language patterns already in the corpus carry a
// dsl_pattern and are NOT style rules, and shape-sniffing would silently
// re-render all of them.
func isStyleRuleNode(n *knowledgev1.Node) bool {
	return kgtypes.Value(n, kgtypes.MetaKeyPracticeKind) == kgtypes.PracticeKindStyleRule
}

// styleRuleReferenceLine renders ONE style rule as its reference line, newline
// included:
//
//   - <id> — severity=<severity> — [scope — ]<summary, capped>
//
// THE ID IS NEVER CAPPED, and that is the one column with no bound. A truncated
// id does not resolve, and resolving it is the entire purpose of a reference —
// every other column exists only to help a reader decide whether to spend a
// lookup on it. The severity, the scope and the summary ARE capped, and all
// three come back in full from that lookup, which is what makes capping them
// cost nothing.
//
// THE SEVERITY IS RENDERED AS STORED, not re-validated against the ladder, and
// its WIDTH IS CAPPED ALL THE SAME. The ladder is enforced where a rule is
// WRITTEN; refusing to render an assembly because one attached rule carries an
// off-ladder value would make the ticket unreadable over exactly the corpus the
// reshape pass has not reached yet. Capping is not validating: a render that
// admits arbitrary VALUES has no basis for assuming a bounded WIDTH: rendered
// uncapped, a 4000-byte severity put a one-rule block 4481 bytes over a bound
// that budgets one column for it.
//
// THE COLUMN CENSUS for this render, which the width tests drive one column at a
// time (style_rule_reference_bounds_test.go):
//
//	column     capped?              why
//	id         UNCAPPED BY DESIGN   a truncated id does not resolve, and resolving it is what a reference is for; the block bound pays it RAW once per rendered reference line and QUOTED once per unique id, each measured at the width that rendering actually spends
//	severity   CAPPED               rendered as stored, never ladder-validated, so its width is corpus data
//	scope      CAPPED               read as raw metadata and decoded nowhere, so its width is corpus data
//	summary    CAPPED               the server admits a 500-rune summary, up to 2000 bytes in one column
//
// Every capped value is recoverable in full from the by-ids read the instruction
// line names, which is what makes capping it cost nothing.
func styleRuleReferenceLine(n *knowledgev1.Node) string {
	cols := []string{
		n.GetId(),
		"severity=" + orAbsent(kgtypes.CapStyleColumn(kgtypes.Value(n, corpus.MetaSeverity))),
	}
	if scope := styleRefScopeColumn(n); scope != "" {
		cols = append(cols, scope)
	}
	cols = append(cols, orAbsent(kgtypes.CapStyleColumn(n.GetSummary())))
	return "- " + strings.Join(cols, styleRefColumnSeparator) + "\n"
}

// orAbsent renders the absent-column marker for an empty value.
func orAbsent(s string) string {
	if s == "" {
		return styleRefAbsentColumn
	}
	return s
}

// styleRefScopeColumn renders a rule's optional repo and path scope, or the
// empty string when the rule carries neither.
//
// IT READS THE TWO KEYS AS STORED AND DECODES NEITHER. The index read decodes
// style_scope_paths and REFUSES a malformed array, which is right for a read
// whose whole subject is style rules: the caller asked about scope and a scope
// that cannot be parsed is not an answer. An assembly is not that read. A ticket
// is assembled to be worked, and giving one attached rule's malformed metadata
// the power to fail the whole assembly would trade a legible hint for an
// unusable page. The raw value is capped like any other column and the by-id
// lookup carries the truth.
func styleRefScopeColumn(n *knowledgev1.Node) string {
	var parts []string
	if repo := kgtypes.Value(n, kgtypes.MetaKeyStyleScopeRepo); repo != "" {
		parts = append(parts, "repo="+repo)
	}
	if paths := kgtypes.Value(n, kgtypes.MetaKeyStyleScopePaths); paths != "" {
		parts = append(parts, "paths="+paths)
	}
	if len(parts) == 0 {
		return ""
	}
	return kgtypes.CapStyleColumn(strings.Join(parts, " "))
}

// renderStyleRuleInstruction writes the single bulk-read instruction for every
// style rule an assembly referenced, or nothing at all when it referenced none.
//
// NOTHING AT ALL IS A REQUIREMENT, not a shortcut: an assembly with no style
// rule attached is byte-identical to what it rendered before this change, which
// is what keeps every existing golden honest.
//
// THE IDS ARE DEDUPLICATED IN FIRST-SEEN ORDER. One rule can reach an assembly
// through both edge types the pattern sections walk, and naming it twice inside
// a by-ids call asks the server for the same node twice while telling the reader
// there are two rules.
func renderStyleRuleInstruction(sb *strings.Builder, ids []string) {
	unique := dedupeStyleRefIDs(ids)
	if len(unique) == 0 {
		return
	}
	quoted := make([]string, 0, len(unique))
	for _, id := range unique {
		quoted = append(quoted, strconv.Quote(id))
	}
	fmt.Fprintf(sb, "\n%s Read them in ONE bulk call — `query(graph:%q, ids:[%s])` — "+
		"never one call per rule, which is what hits the tool's response limits.\n",
		styleRefInstructionMarker, "practice", strings.Join(quoted, ", "))
}

// dedupeStyleRefIDs drops repeats while preserving first-seen order.
func dedupeStyleRefIDs(ids []string) []string {
	seen := make(map[string]struct{}, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

// styleRefBlockBound is the declared byte bound for the block an assembly emits
// for a set of referenced style rules: every reference line plus the one
// instruction line.
//
// IT IS ONE PLACE RATHER THAN ONE PER ASSERTION, exactly as the index read's own
// styleIndexBlockIsWithinBound is: a bound restated in each test that reads it is
// a bound that drifts.
//
// THE TERM TABLE, and every term is MEASURED, CAPPED or FIXED — never BUDGETED.
// A term is MEASURED when the bound reads the actual input and pays what it is
// worth, CAPPED when this render controls the width itself, and FIXED when the
// render emits a literal. A BUDGETED term is a width or a count the bound
// assumes and nothing constrains, and this bound has none.
//
// THE SUMMATION IS A TERM TOO, and it is the column four review rounds cost. A
// bound over a BLOCK has a per-item term list AND an item count; classifying
// every term of one line says nothing about how many lines the sum runs over.
// The two summations here are different, because the two things being summed are
// rendered on different schedules: a reference line is written once per
// COLLECTED id and the instruction names each id ONCE. So the table carries the
// schedule beside the kind:
//
//	term                                kind      summed over         why
//	styleRefInstructionBound            FIXED     once per block      the instruction's prose is a literal in this file, and there is one instruction line per assembly
//	kgtypes.StyleIndexRowBound(id)      (a bound) per RENDERED LINE   covers one reference line: MEASURED in the id, CAPPED in the other four columns, FIXED in the separators
//	len(strconv.Quote(id))              MEASURED  per UNIQUE id       the instruction spends the id's QUOTED form, whose width is the id's escaping and not its length
//	styleRefInstructionIDSeparatorBytes FIXED     per UNIQUE id       the `, ` this render writes between two ids
//
// WHY A LINE COUNT IS NOT ONE PER RULE. ticket.go walks ONE outgoing-edge list
// and dispatches EdgeUses into renderTicketPatterns and EdgeAudits into
// renderLanguagePatternsSection; a rule carrying both edge types lands in both
// slices and renders a reference line in both sections, and ticket.go
// concatenates the two id slices into the single instruction. Two lines, one
// instruction token — and NO CEILING ON THAT COUNT IS CLAIMED HERE OR RELIED ON.
// The loop below sums a row bound over every line the sections collected,
// whatever the count, so an id reached three or more times is paid three or more
// times. An earlier revision of this comment justified a two-line ceiling by
// asserting a property of another subsystem, which this bound neither reads nor
// needs: a bound that sums what it observed is true at any line count.
//
// AN ID IS RENDERED IN TWO SHAPES AND EACH IS PAID AT ITS OWN WIDTH ON ITS OWN
// SCHEDULE. A reference line writes it raw, so the row bound's own len(id) term
// covers it; a reference line's columns are the index row's minus the
// sister-check id, so that bound covers the line with room to spare. The
// instruction line writes it through strconv.Quote, which is a different width
// for the same bytes — a double quote and a backslash cost two, a control byte
// four — so that occurrence is paid at its rendered width instead. Nothing here
// budgets a width, and nothing here budgets a count.
//
// ZERO RULES BOUND ZERO BYTES, and that arm is not a special case bolted on: an
// assembly with no style rule emits no reference line AND no instruction line,
// so the honest bound for that input class is 0 rather than the instruction
// line's prose.
func styleRefBlockBound(ids []string) int {
	unique := dedupeStyleRefIDs(ids)
	if len(unique) == 0 {
		return 0
	}
	bound := styleRefInstructionBound
	// ONE ROW BOUND PER RENDERED LINE, so the raw slice rather than the deduped
	// one: the sections write a reference line for every id they collected.
	for _, id := range ids {
		bound += kgtypes.StyleIndexRowBound(id)
	}
	// ONE QUOTED TOKEN PER UNIQUE ID, because renderStyleRuleInstruction
	// deduplicates before it quotes and joins.
	for _, id := range unique {
		bound += len(strconv.Quote(id)) + styleRefInstructionIDSeparatorBytes
	}
	return bound
}

// styleRefBlockIsWithinBound reports whether a rendered block is at or under its
// declared bound.
func styleRefBlockIsWithinBound(ids []string, block string) bool {
	return len(block) <= styleRefBlockBound(ids)
}
