// SPDX-License-Identifier: Apache-2.0

package recipe

import (
	"fmt"
	"sort"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/ast"
)

// source_census.go — the vocabulary a loaded source graph actually carries, and
// the near-miss wording a refusal uses when a recipe names something outside it.
//
// THE CENSUS IS THE CORPUS, NOT A SCHEMA. Every value here was observed on a
// node or an edge of the graph this run loaded. That is the opposite of the ast
// package's kind vocabulary, which comes from a tree-sitter grammar and admits
// kinds no file uses; ast.ClosestVocabulary's doc comment records why the two
// validators mirror each other's shape and can never be merged.
//
// Cost: one O(nodes + edges) pass over maps loadSourceView has already
// materialized, memoized per view, so the census adds zero wire round trips and
// never runs inside a row loop.

// The vocabularies suggest can be asked about. They are the human-readable
// labels a refusal prints, so they double as the selector — every call site
// passes one of these three constants.
const (
	censusNodeType = "node type"
	censusEdgeType = "edge type"
	censusMetaKey  = "metadata key"

	// censusEdgeEvidenceKey is a FOURTH vocabulary rather than a reuse of the
	// metadata-key one, because an edge's Evidence and a node's Metadata are
	// different maps written by different emitters. Folding them would admit
	// `edge.page_first`, which no edge carries, while refusing `edge.position`,
	// which no node carries.
	censusEdgeEvidenceKey = "edge evidence key"
)

// sourceCensus is the vocabulary of one loaded source graph: every node type,
// every edge type, every metadata key and every EDGE EVIDENCE key it carries,
// each sorted so a refusal listing them reads identically run to run.
//
// EDGE TYPES ARE COLLECTED VERBATIM AND NEVER FOLDED. A web raw graph carries
// kgtypes.EdgeContains ("CONTAINS") for content containment and
// kgtypes.EdgeKGContains ("contains") for the github root AT THE SAME TIME, and
// live recipes traverse each. Folding the two casings together would merge two
// distinct edge families and silently admit a recipe that matches nothing.
type sourceCensus struct {
	nodeTypes        []string
	edgeTypes        []string
	metaKeys         []string
	edgeEvidenceKeys []string
}

// census returns the memoized vocabulary of this view, building it on first
// call.
//
// The build is behind a sync.Once because every validation site asks for the
// same answer and the walk is O(nodes + edges); the counter that proves the walk
// happened once lives in the BUILDER, not here, so a caller that bypasses this
// method and walks the indexes itself is countable rather than invisible.
func (sv *sourceView) census() *sourceCensus {
	sv.censusOnce.Do(func() {
		sv.censusCached = sv.buildCensus()
	})
	return sv.censusCached
}

// buildCensus walks the materialized indexes once and collects the three
// vocabularies.
//
// THE WALK COUNTER IS INCREMENTED HERE AND NOWHERE ELSE. Incrementing it in
// census() would count CALLS, which sync.Once already makes uninteresting; what
// a test needs to observe is how many times the graph was actually WALKED, so a
// validator that ranges byType / outEdges / byID inline at each of its sites is
// visible as a walk count of zero against a census that was never consulted.
func (sv *sourceView) buildCensus() *sourceCensus {
	sv.censusWalks++

	c := &sourceCensus{}

	// nodeTypes: loadSourceView appends every non-nil node to byType[n.Type], so
	// the key set is exactly the types this graph carries.
	c.nodeTypes = make([]string, 0, len(sv.byType))
	for t := range sv.byType {
		c.nodeTypes = append(c.nodeTypes, t)
	}
	sort.Strings(c.nodeTypes)

	// edgeTypes: loadSourceView appends EVERY fetched edge to outEdges[e.FromId]
	// before also indexing it into inEdges, so iterating outEdges alone reaches
	// every edge exactly once. Iterating inEdges as well would double-walk for no
	// new members.
	//
	// The EDGE EVIDENCE KEYS are collected on this same pass rather than on a
	// second one. buildCensus's counter is incremented here and nowhere else
	// precisely so an extra traversal is visible rather than invisible, and a
	// separate walk for the fourth vocabulary would double the census's cost for
	// keys the edges in hand already carry. An edge whose Evidence is opaque —
	// the treesitter collector's `import:` and `flow:` forms — decodes to nil and
	// contributes nothing, which is a property of that corpus and not a refusal.
	seenEdge := make(map[string]struct{})
	seenEvidenceKey := make(map[string]struct{})
	for _, edges := range sv.outEdges {
		for _, e := range edges {
			seenEdge[e.Type] = struct{}{}
			for k := range edgeEvidenceMap(e.Evidence) {
				seenEvidenceKey[k] = struct{}{}
			}
		}
	}
	c.edgeEvidenceKeys = make([]string, 0, len(seenEvidenceKey))
	for k := range seenEvidenceKey {
		c.edgeEvidenceKeys = append(c.edgeEvidenceKeys, k)
	}
	sort.Strings(c.edgeEvidenceKeys)
	c.edgeTypes = make([]string, 0, len(seenEdge))
	for t := range seenEdge {
		c.edgeTypes = append(c.edgeTypes, t)
	}
	sort.Strings(c.edgeTypes)

	// metaKeys: the GRAPH-WIDE union, not a per-node-type one. A recipe
	// legitimately reads a key only one node type stamps — a per-type census
	// would refuse that correct recipe, which is the wrong-but-compiling
	// implementation this vocabulary exists to avoid.
	seenKey := make(map[string]struct{})
	for _, n := range sv.byID {
		for k := range n.Metadata {
			seenKey[k] = struct{}{}
		}
	}
	c.metaKeys = make([]string, 0, len(seenKey))
	for k := range seenKey {
		c.metaKeys = append(c.metaKeys, k)
	}
	sort.Strings(c.metaKeys)

	return c
}

// vocabulary returns the observed values for one of the three census kinds, and
// whether the kind is one this census knows.
func (c *sourceCensus) vocabulary(kind string) ([]string, bool) {
	switch kind {
	case censusNodeType:
		return c.nodeTypes, true
	case censusEdgeType:
		return c.edgeTypes, true
	case censusMetaKey:
		return c.metaKeys, true
	case censusEdgeEvidenceKey:
		return c.edgeEvidenceKeys, true
	}
	return nil, false
}

// suggest returns the near-miss clause for a value that is NOT in the named
// vocabulary, or "" when nothing plausible is close enough to name.
//
// IT DECIDES PROSE, NEVER MEMBERSHIP. Every accept/reject comparison in
// validate_source.go is exact-case; `contains` stays refused against a graph
// carrying only `CONTAINS`. Nothing on the membership path may call this, and
// nothing here may be reused to decide one.
//
// TWO PASSES, IN THIS ORDER.
//
// PASS 1, CASE-INSENSITIVE EXACT MATCH. A case flip changes every byte, so the
// edit distance equals the whole word length: measured against the real
// vocabulary, ClosestVocabulary("contains", ["CONTAINS"]) returns nothing at
// distance 8, and "Contains" nothing at distance 7. The suggester is therefore
// structurally incapable of naming a case-differing sibling — and a mis-cased
// edge type is the headline measured failure this validator exists to catch, so
// without this pass its refusal would name no near-miss at all.
//
// IT NAMES EVERY SIBLING, NOT THE FIRST. A web raw graph carries `CONTAINS` for
// content containment and `contains` for the github root AT THE SAME TIME, and
// live recipes traverse each — that two-family shape is the whole reason
// membership is exact-case. Returning on the first match would answer a
// `Contains` typo with `CONTAINS` alone, purely because the vocabulary sorts
// that way, and an author who takes the suggestion and stops would traverse the
// wrong family and get wrong rows with no error. Where the graph really is
// ambiguous, the honest output is the candidate SET.
//
// PASS 2, EDIT DISTANCE. ast.ClosestVocabulary, bounded at three edits, which is
// what names an ordinary typo: "sectionn" against the pdf node types returns
// "section".
//
// NEITHER PASS MATCHING RETURNS "", so a refusal never invents advice. An
// unknown kind likewise yields no clause: this function formats an explanation
// and has no answer to give about a vocabulary it does not hold.
func (c *sourceCensus) suggest(kind, value string) string {
	observed, ok := c.vocabulary(kind)
	if !ok {
		return ""
	}

	var siblings []string
	for _, candidate := range observed {
		if strings.EqualFold(candidate, value) {
			siblings = append(siblings, candidate)
		}
	}
	switch len(siblings) {
	case 0:
		// fall through to the edit-distance pass
	case 1:
		return fmt.Sprintf(
			"did you mean %q? the source graph carries that %s with different casing, and %ss are matched exactly",
			siblings[0], kind, kind)
	default:
		return fmt.Sprintf(
			"did you mean %s? the source graph carries that %s in more than one casing, and %ss are matched exactly",
			quoteJoin(siblings), kind, kind)
	}

	near := ast.ClosestVocabulary(value, observed)
	if len(near) == 0 {
		return ""
	}
	return fmt.Sprintf("did you mean %s?", quoteJoin(near))
}

// quoteJoin renders a candidate set as quoted alternatives.
func quoteJoin(values []string) string {
	quoted := make([]string, 0, len(values))
	for _, v := range values {
		quoted = append(quoted, fmt.Sprintf("%q", v))
	}
	return strings.Join(quoted, " or ")
}
