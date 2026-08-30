// SPDX-License-Identifier: Apache-2.0

// manage_checks_list.go — the checks-graph inventory.
//
// WHY IT EXISTS AT ALL. query(mode:"stats", graph:"checks") is refused, and
// ranked search over the checks graph answers a DIFFERENT question: search finds
// a check by intent, while this answers which finding is a check, which example
// is bound to it, which entries need LLM judgment, which are incompletely
// authored, and which fixtures nothing binds. The last of those is the honest-
// inability surface — an orphaned fixture is reachable by no executor and, by
// design, by no ranked search either, so nothing but this would ever name it.

package tools

import (
	"context"
	"fmt"
	"sort"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/corpus"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

// checksGraphListName is the instance name sent on the wire for the ONE checks
// graph, and it is deliberately EMPTY: checks is a singleton whose selector
// policy consumes no instance field and REJECTS a set name.
const checksGraphListName = ""

// maxListedRows caps how many rows one list call renders per section.
//
// DERIVED, NOT CHOSEN, and from the same measurement corpus_scan's ceilings come
// from (corpusscan/doc.go's render-ceiling section): the MCP tool-result surface
// refused a ~52,000-character payload, and a rendered row here is one line of
// roughly 200 characters, so 200 rows per section sits an order of magnitude
// under the refusal while covering every corpus this has been run against.
// Truncation is DISCLOSED with the true total rather than silently applied.
const maxListedRows = 200

// manageChecksList renders the checks-graph inventory.
//
// TWO BOUNDED WHOLE-TYPE DRAINS PLUS ONE IN-MEMORY JOIN — the shape the corpus
// loader already justifies: one extra bounded drain buys a map that answers
// every fixture lookup without putting a per-check read on an unverified path.
// There is no per-check wire read.
func manageChecksList(ctx context.Context, gc GraphCaller, a manageChecksArgs) kgtools.ToolResult {
	// An ABSENT language lists every language rather than defaulting to one: the
	// inventory's whole job is to show what is there, and a silent default would
	// hide every other language's corpus behind an empty-looking answer.
	var meta map[string]string
	if a.Language != "" {
		meta = map[string]string{corpus.MetaLanguage: a.Language}
	}
	checkNodes, err := foundation.FetchNodesByTypeMeta(
		ctx, gc, kgtypes.GraphChecks, checksGraphListName, kgtypes.NodeFinding, meta)
	if err != nil {
		return errorResult("manage_checks list: read checks: " + err.Error())
	}
	exampleNodes, err := foundation.FetchNodesByTypeMeta(
		ctx, gc, kgtypes.GraphChecks, checksGraphListName, kgtypes.NodeExample, meta)
	if err != nil {
		return errorResult("manage_checks list: read fixtures: " + err.Error())
	}
	return textResult(renderChecksInventory(checkNodes, exampleNodes, a.Language))
}

// checkRow is one decoded check-graph finding, classified by the contract's own
// return table.
type checkRow struct {
	id   string
	line string
	// lane files the row under one of ParseCheck's four return rows. Keeping
	// them apart is the contract's explicit requirement: collapsing rows 2 and 3
	// destroys the accepted-llm_only lane, and collapsing row 4 into either hides
	// a corpus an operator must fix.
	lane string
}

// The four lanes, named once. They are the section headings AND the sort key, so
// a lane rename cannot leave one of the two behind.
const (
	laneExecutable  = "executable checks"
	laneLLMOnly     = "accepted llm_only (needs LLM judgment — not executed)"
	laneUnauthored  = "incompletely authored (neither check_type nor llm_only)"
	laneUnadmitted  = "refused by the check contract"
	laneUnboundFixt = "example nodes bound by no check"
)

// renderChecksInventory builds the whole rendering.
func renderChecksInventory(checkNodes, exampleNodes []*knowledgev1.Node, language string) string {
	examples := make(map[string]*knowledgev1.Node, len(exampleNodes))
	for _, n := range exampleNodes {
		examples[n.GetId()] = n
	}
	bound := map[string]bool{}

	lanes := map[string][]checkRow{}
	for _, n := range checkNodes {
		row := classifyCheckNode(n, examples, bound)
		lanes[row.lane] = append(lanes[row.lane], row)
	}

	var out strings.Builder
	scope := "every language"
	if language != "" {
		scope = corpus.MetaLanguage + "=" + language
	}
	fmt.Fprintf(&out, "checks graph inventory (%s): %d check node(s), %d example node(s)\n",
		scope, len(checkNodes), len(exampleNodes))

	for _, lane := range []string{laneExecutable, laneLLMOnly, laneUnauthored, laneUnadmitted} {
		writeLane(&out, lane, lanes[lane])
	}

	// THE UNBOUND-FIXTURE SECTION IS NOT AN EXTRA. A renderer that only walked
	// checks would call itself complete while leaving every orphaned example
	// invisible — reachable by no executor, and after the search cutover by no
	// ranked search either.
	var unbound []checkRow
	for _, n := range exampleNodes {
		if bound[n.GetId()] {
			continue
		}
		unbound = append(unbound, checkRow{
			id:   n.GetId(),
			line: fmt.Sprintf("  %s  %s", n.GetId(), displayName(n)),
		})
	}
	writeLane(&out, laneUnboundFixt, unbound)
	return out.String()
}

// classifyCheckNode runs one node through the contract and files it under the
// return row it landed on, marking any fixture it binds as bound.
//
// THE BRANCH ORDER IS LOAD-BEARING: error first, then isCheck, then LLMOnly,
// then the neither-key case. A reader that tests isCheck first and skips
// everything else merges rows 2 and 3 and silently destroys the llm_only lane —
// a collapse no source grep distinguishes from correct code.
func classifyCheckNode(n *knowledgev1.Node, examples map[string]*knowledgev1.Node, bound map[string]bool) checkRow {
	c, isCheck, err := corpus.ParseCheck(n)
	switch {
	case err != nil:
		// Row 4. Relay the contract's own message verbatim; it is the only thing
		// that tells an operator what to fix.
		return checkRow{id: n.GetId(), lane: laneUnadmitted,
			line: fmt.Sprintf("  %s  %s\n      %v", n.GetId(), displayName(n), err)}
	case isCheck:
		bound[c.FixtureBad] = true
		bound[c.FixtureGood] = true
		return checkRow{id: n.GetId(), lane: laneExecutable, line: fmt.Sprintf(
			"  %s  %s\n      %s=%s %s=%s %s=%s\n      %s=%s (%s)\n      %s=%s (%s)",
			n.GetId(), displayName(n),
			corpus.MetaLanguage, c.Language, corpus.MetaCheckType, c.Type, corpus.MetaSeverity, c.Severity,
			corpus.MetaFixtureBad, c.FixtureBad, fixtureName(examples, c.FixtureBad),
			corpus.MetaFixtureGood, c.FixtureGood, fixtureName(examples, c.FixtureGood))}
	case c.LLMOnly:
		// Row 2. Its own lane, never folded into the skip below — the corpus's
		// needs-LLM-judgment population must never be invisible.
		return checkRow{id: n.GetId(), lane: laneLLMOnly, line: fmt.Sprintf(
			"  %s  %s\n      %s=%s", n.GetId(), displayName(n), corpus.MetaLanguage, c.Language)}
	default:
		// Row 3. A finding in the checks graph carrying neither contract key is
		// an incompletely-authored node, not a different kind of content.
		return checkRow{id: n.GetId(), lane: laneUnauthored,
			line: fmt.Sprintf("  %s  %s", n.GetId(), displayName(n))}
	}
}

// fixtureName resolves a bound fixture id to its node name, so a reader is not
// left holding two opaque ids. An id resolving to nothing is stated rather than
// rendered blank — an unresolvable binding is exactly what the operator needs to
// see.
func fixtureName(examples map[string]*knowledgev1.Node, id string) string {
	n, ok := examples[id]
	if !ok {
		return "UNRESOLVED — no example node with this id in the checks graph"
	}
	return displayName(n)
}

// displayName reads a node's human name, falling back to its id when a corpus
// author left it unnamed.
func displayName(n *knowledgev1.Node) string {
	if name := strings.TrimSpace(n.GetSymbolName()); name != "" {
		return name
	}
	return n.GetId()
}

// writeLane renders one section, sorted by id and bounded by maxListedRows with
// the true total disclosed when the ceiling fires.
func writeLane(out *strings.Builder, lane string, rows []checkRow) {
	fmt.Fprintf(out, "\n%s: %d\n", lane, len(rows))
	sort.Slice(rows, func(i, j int) bool { return rows[i].id < rows[j].id })
	for i, r := range rows {
		if i >= maxListedRows {
			fmt.Fprintf(out, "  ... %d of %d rendered; the rest are withheld by this tool's render ceiling\n",
				maxListedRows, len(rows))
			return
		}
		fmt.Fprintln(out, r.line)
	}
}
