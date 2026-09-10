// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"fmt"
	"strings"
)

// collect_context_report.go — WHAT THE FILL ACTUALLY SELECTED, per declared
// family, per graph instance and per declared node type.
//
// THE PAIR IT SEPARATES, AND WHY NOTHING ELSE DOES. Two states of the filled
// block are byte-identical in one call: a graph holding zero nodes OF THE
// DECLARED TYPE, and a graph that is genuinely empty. Both render as
// `{"aws":[{"graph_name":"prod","nodes":null,"edges":null}]}`. So a declaration
// naming a type no collector emits — which is what four shipped declarations
// did — produces exactly the output an operator with an empty inventory graph
// sees, and there is nothing in a successful collect to tell them apart. Every
// other outcome on this path is already loud: an unregistered family is a
// refusal naming it and listing the supplyable set, and a supplyable family with
// no graph instances is a present key holding an empty array. This one was
// silent, and it stayed silent through a whole release.
//
// A ZERO IS RENDERED AS A ZERO. That is the requirement, not a formatting
// preference: the report exists so "the type I declared matched nothing here" is
// readable, and a row omitted because its count was zero would report exactly
// the state the report was written to expose.
//
// A FAMILY DECLARING NO NODE TYPES IS A THIRD STATE and is NOT rendered as a
// zero match. Such a declaration legitimately carries the graph NAMES and no
// nodes — the whole input a membership test over graph names needs — so it
// renders as the graph with a names-only note and no per-type row. Reporting it
// as "matched 0" would name a defect where there is a deliberate declaration.
//
// IT REPORTS AND IT NEVER NARROWS. Nothing here is a size cap or a filter: the
// counts are read off slices the fill already built, and the block a module
// receives is byte-identical whether the report is rendered or not.

// everyNodeTypeLabel is the row label a family declaring all_node_types reports
// under. It is not a type any collector emits, and it is spelled so it cannot be
// mistaken for one: the declaration named no type, so there is no type string to
// print, and printing nothing would make the row unreadable.
const everyNodeTypeLabel = "(every type)"

// contextTypeFill is one declared node type's match count in one graph.
type contextTypeFill struct {
	// NodeType is the type string the declaration named, verbatim.
	NodeType string
	// Matched is how many nodes of that type survived the projection into the
	// block. It is the length of what was carried, not what was fetched.
	Matched int
}

// contextGraphFill is one graph instance of one declared family.
type contextGraphFill struct {
	// Name is the graph instance's own name, as the read that filled it named it.
	Name string
	// Types are the declared types in DECLARATION ORDER, one row each including
	// the ones that matched nothing.
	Types []contextTypeFill
}

// contextFamilyFill is one declared family's fill.
type contextFamilyFill struct {
	// Family is the declared family name.
	Family string
	// Graphs are the family's graph instances, in the order the fill read them,
	// which the fill sorts. Empty means the daemon holds no graph of this type,
	// which is a legitimate answer and is reported as such.
	Graphs []contextGraphFill
	// NamesOnly records that the declaration named NO node types, so the family
	// carries graph names and nothing else by design. It is a separate field
	// rather than an inference from an empty Types slice, because "declared no
	// types" and "declared types that all matched nothing" are opposite facts and
	// collapsing them would be the same silence this report exists to break.
	NamesOnly bool
}

// contextFillReport is the whole fill's report, families in the order the fill
// walked them (sorted).
type contextFillReport struct {
	Families []contextFamilyFill
}

// Render formats the report as a one-line collect-result suffix:
//
//	foreign context: aws/prod cloud-resource 3; azure/prod cloud-resource 0; code (no graphs); gcp/prod (names only)
//
// AN EMPTY REPORT RENDERS THE EMPTY STRING, which is the contract withComposition
// already carries: an empty addendum degrades to the result text unchanged, so a
// collect whose entry declares no context prints byte-identically to before this
// report existed.
//
// THE ORDER IS THE FILL'S OWN, and the fill sorts both its family keys and its
// graph names so one unchanged store yields one byte-identical block across
// runs. Re-sorting here would be a second ordering rule that could disagree with
// the block's; reading the fill's order keeps the report and the block the same
// walk.
func (r *contextFillReport) Render() string {
	if r == nil || len(r.Families) == 0 {
		return ""
	}
	parts := make([]string, 0, len(r.Families))
	for _, family := range r.Families {
		if len(family.Graphs) == 0 {
			// DECLARED AND ANSWERED WITH NOTHING. The family is supplyable — an
			// unsupplyable one never reaches here, it is refused by name — and this
			// daemon holds no graph of it. That is an answer the module is entitled
			// to see, so it is reported rather than omitted.
			parts = append(parts, family.Family+" (no graphs)")
			continue
		}
		for _, graph := range family.Graphs {
			where := family.Family + "/" + graph.Name
			if family.NamesOnly {
				parts = append(parts, where+" (names only)")
				continue
			}
			counts := make([]string, 0, len(graph.Types))
			for _, t := range graph.Types {
				counts = append(counts, fmt.Sprintf("%s %d", t.NodeType, t.Matched))
			}
			parts = append(parts, where+" "+strings.Join(counts, ", "))
		}
	}
	return "foreign context: " + strings.Join(parts, "; ") + "."
}
