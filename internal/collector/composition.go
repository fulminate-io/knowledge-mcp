// SPDX-License-Identifier: Apache-2.0

package collector

import (
	"fmt"
	"sort"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/collectorwire"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// compositionRenderTopN bounds how many distinct node types Render names.
//
// Render is a bounded accumulation, not an unbounded one: a code collect emits
// tree-sitter grammar node names as chunk types, so the distinct-type count is
// easily dozens and an unbounded render would make the collect success line
// enormous. Types beyond the bound are summarized by the ", +<K> more types"
// truncation signal. TotalNodes and TotalEdges are never truncated.
const compositionRenderTopN = 12

// CollectComposition is the node-type census of one collection run: what the
// harvest actually produced, as opposed to how many rows it wrote.
//
// A count of nodes written is to a harvest what a green test run is to a check
// that never executed — insensitive to the thing that matters. The composition
// is the signal a caller needs to tell a real harvest from one that captured
// only navigation chrome, and it is what the per-collector invariant in
// CheckComposition reads.
type CollectComposition struct {
	GraphType   kgtypes.GraphType
	GraphName   string
	TotalNodes  int
	TotalEdges  int
	NodesByType map[string]int
	// NonSubstantiveNodes is carried through verbatim from the result the
	// producing collector built: how many nodes carry a content type while
	// being retained chrome. It is READ ONLY BY THE PRODUCING COLLECTOR'S OWN
	// INVARIANT, which knows what the class means; nothing generic here
	// interprets it, and Render does not name it, because it is a correction
	// to one collector's arithmetic rather than a fact about the census.
	NonSubstantiveNodes int
	// Degraded is the producing collector's per-class census of dropped work,
	// carried through verbatim and RENDERED, so a caller reading the collect
	// response sees what the harvest lost rather than only what it captured.
	Degraded map[string]int
	// GithubFollowUps / GithubFollowUpSample carry the producing collector's
	// follow-up inventory through to the rendered response. See the fields of
	// the same name on collectorwire.CollectResult for why this is not a
	// degrade.
	GithubFollowUps      int
	GithubFollowUpSample []string
}

// NewCollectComposition censuses a collect result by node Type.
//
// One serial O(N) pass over result.Nodes into a map. Serial is correct: this is
// a single in-process pass over a slice the collect already materialized and
// the sink is about to range anyway, so it is invisible next to the parse and
// upload it sits between.
//
// A nil result yields the zero composition — TotalNodes 0 — which is the same
// shape an all-fetches-failed harvest produces and which the web invariant's
// zero-node leg reports as a failure. It is not smoothed over.
func NewCollectComposition(result *collectorwire.CollectResult) CollectComposition {
	if result == nil {
		return CollectComposition{}
	}
	byType := make(map[string]int)
	for _, n := range result.Nodes {
		if n == nil {
			continue
		}
		byType[n.Type]++
	}
	return CollectComposition{
		GraphType:           result.GraphType,
		GraphName:           result.GraphName,
		TotalNodes:          len(result.Nodes),
		TotalEdges:          len(result.Edges),
		NodesByType:         byType,
		NonSubstantiveNodes: result.NonSubstantiveNodes,
		Degraded:            result.Degraded,

		GithubFollowUps:      result.GithubFollowUps,
		GithubFollowUpSample: result.GithubFollowUpSample,
	}
}

// Render formats the composition as a one-line tool-summary suffix:
//
//	nodes 64 (list_item 24, link 16, table 12, list 4, page 4, section 4), edges 60
//
// A composition with no node types renders "nodes 0, edges 0" — no empty
// parenthetical. Beyond compositionRenderTopN distinct types the list is
// truncated to the top N and carries a trailing ", +<K> more types".
//
// The ordering rule — count descending, then name ascending — is the repo's
// existing by-type breakdown ordering from
// cmd/knowledge/internal/engine/render_stats.go:61-70. The inline "type n" form
// differs from that function's bulleted block only because this is a one-line
// suffix rather than a markdown section.
func (c CollectComposition) Render() string {
	if len(c.NodesByType) == 0 {
		return fmt.Sprintf("nodes %d, edges %d", c.TotalNodes, c.TotalEdges)
	}

	type row struct {
		name  string
		count int
	}
	rows := make([]row, 0, len(c.NodesByType))
	for name, n := range c.NodesByType {
		rows = append(rows, row{name: name, count: n})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].count != rows[j].count {
			return rows[i].count > rows[j].count
		}
		return rows[i].name < rows[j].name
	})

	omitted := 0
	if len(rows) > compositionRenderTopN {
		omitted = len(rows) - compositionRenderTopN
		rows = rows[:compositionRenderTopN]
	}

	parts := make([]string, 0, len(rows))
	for _, r := range rows {
		parts = append(parts, fmt.Sprintf("%s %d", r.name, r.count))
	}
	list := strings.Join(parts, ", ")
	if omitted > 0 {
		list += fmt.Sprintf(", +%d more types", omitted)
	}
	return fmt.Sprintf("nodes %d (%s), edges %d", c.TotalNodes, list, c.TotalEdges) +
		c.renderDegraded() + c.renderGithubFollowUps()
}

// renderGithubFollowUps formats the follow-up inventory as a suffix:
//
//	, github follow-ups 5 (https://a, https://b, https://c, +2 more)
//
// A HARVEST THAT MET NO GITHUB LINK RENDERS NOTHING, on the same reasoning as
// the degrade suffix: a line printed on every collect is a line nobody reads.
//
// THE COUNT IS EXACT AND THE SAMPLE IS CAPPED. The "+K more" states how many
// URLs were omitted, so the sample can never be mistaken for the whole
// inventory — which is the failure a silently truncated list would produce.
func (c CollectComposition) renderGithubFollowUps() string {
	if c.GithubFollowUps == 0 {
		return ""
	}
	out := fmt.Sprintf(", github follow-ups %d", c.GithubFollowUps)
	if len(c.GithubFollowUpSample) == 0 {
		return out
	}
	sample := strings.Join(c.GithubFollowUpSample, ", ")
	if omitted := c.GithubFollowUps - len(c.GithubFollowUpSample); omitted > 0 {
		sample += fmt.Sprintf(", +%d more", omitted)
	}
	return out + " (" + sample + ")"
}

// renderDegraded formats the per-class degrade census as a suffix:
//
//	, degraded (fetch_failed 20, content_alias 1)
//
// A COMPOSITION WITH NO DEGRADATIONS RENDERS NOTHING EXTRA, so a clean
// harvest's response is byte-identical to what it was before the census
// existed. That is deliberate: a "degraded (none)" suffix on every successful
// collect would train a reader to skip the line that matters.
//
// The ordering is count-descending then name-ascending — the same rule the
// node-type list above uses, so one response does not carry two orderings.
func (c CollectComposition) renderDegraded() string {
	if len(c.Degraded) == 0 {
		return ""
	}
	type row struct {
		name  string
		count int
	}
	rows := make([]row, 0, len(c.Degraded))
	for name, n := range c.Degraded {
		if n <= 0 {
			continue
		}
		rows = append(rows, row{name: name, count: n})
	}
	if len(rows) == 0 {
		return ""
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].count != rows[j].count {
			return rows[i].count > rows[j].count
		}
		return rows[i].name < rows[j].name
	})
	parts := make([]string, 0, len(rows))
	for _, r := range rows {
		parts = append(parts, fmt.Sprintf("%s %d", r.name, r.count))
	}
	return ", degraded (" + strings.Join(parts, ", ") + ")"
}

// CompositionAsserter is the OPTIONAL per-collector capability declaring what a
// usable harvest of that source must contain. A collector that implements it has
// its composition checked before collect reports success; one that does not is
// silent and gets the composition line with no gate.
//
// Type-asserted rather than a required Collector method, the SAME
// optional-capability-by-structural-typing discipline as
// cmd/knowledge/internal/tools/collect.go:307 (pipelineWaker) and
// cmd/knowledge/internal/tools/manage_collect_runs.go:22 (collectRunReporter):
// no existing collector is forced to declare an invariant it does not have, and
// the dispatch degrades to nothing when the assert misses.
type CompositionAsserter interface {
	AssertComposition(c CollectComposition) error
}

// CheckComposition runs the registered collector's composition invariant, if it
// declares one. It returns nil when the collector type is not registered at all
// and when the registered collector does not implement CompositionAsserter —
// the degrade-to-nothing half of the capability idiom.
//
// It is called ONCE, at the single top-level collect site, deliberately NOT
// inside Collect: the four cloud collectors call Collect recursively per cascade
// target, so an assertion inside Collect would fire per cascade sub-collect and
// let a sub-collect's composition fail the parent.
func CheckComposition(collectorType string, c CollectComposition) error {
	col, err := Lookup(collectorType)
	if err != nil {
		// An unregistered collector type declares no invariant, so there is
		// nothing to assert. Returning the lookup error here would convert
		// "this type has no gate" into "this collect failed", which is the
		// opposite of the degrade-to-nothing contract this helper carries.
		return nil //nolint:nilerr // deliberate: absence of a registration is absence of a gate, not a failure
	}
	a, ok := col.(CompositionAsserter)
	if !ok {
		return nil
	}
	return a.AssertComposition(c)
}
