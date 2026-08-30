// SPDX-License-Identifier: Apache-2.0

// checks.go reads the checks corpus: the check nodes, their fixture examples,
// and the decode of each node through the contract's own parser.

package corpusscan

import (
	"context"
	"fmt"
	"sort"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/corpus"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

// corpusEntry pairs a check's MACHINE half with its PROSE half.
//
// RETAINING THE NODE IS NOT OPTIONAL. corpus.Check is deliberately the machine
// half only — the contract's package doc says so — so SymbolName, Description
// and Content live on the source node and nowhere else. Finding.Title is built
// from the check's SymbolName and Finding.Summary from its guidance text, so a
// reader that dropped the nodes could not name the check it reports on.
type corpusEntry struct {
	// Check is the machine half: type, severity, language, pattern, where,
	// fixture ids and ID.
	Check corpus.Check
	// Node is the prose half: SymbolName, Description, Content.
	Node *knowledgev1.Node
}

// corpusSet is one checks graph's corpus, decoded.
type corpusSet struct {
	// Checks are contract return row 1 — executable checks, sorted by Check.ID.
	Checks []corpusEntry
	// LLMOnly are contract return row 2 — nodes the contract ACCEPTED and
	// deliberately marked as needing LLM judgment. Sorted by ID. It is NOT
	// narrowed by the checks subset: the disclosure describes the corpus that
	// was READ rather than the subset that RAN, and narrowing it would make the
	// lane count silently depend on an unrelated param.
	LLMOnly []corpus.Check
	// Fixtures is every example node in the graph, keyed by node id.
	Fixtures map[string]corpus.Fixture
}

// checksGraphName is the instance name sent on the wire for the ONE checks
// graph, and it is deliberately EMPTY.
//
// checks is a singleton, so the server's selector policy declares it consumes NO
// instance field and REJECTS a set name — sending the literal "default" would be
// refused by validateGraphSelector before routing, because a name implies a
// per-instance family that does not exist. The server maps the singleton to its
// own internal "default" store name; that is its business, not the wire's.
//
// foundation.scopePayload turns an empty name into a bare {"graph":"checks"}
// payload, which is exactly the selector the singleton arm expects.
const checksGraphName = ""

// fetchCorpus reads and decodes the checks corpus for one language.
//
// READS FROM THE SINGLE checks GRAPH, NOT practice/<language> and not a
// per-language checks instance. Every check carries `language` as one of the
// contract's own eight keys, so language narrows a SUBSET of this graph via a
// server-side metadata predicate. The practice graph is not consulted and there
// is no fallback to it — a check authored into practice/<language> is simply not
// found, which is what makes the retarget observable.
//
// THE FILTER IS NOT COSMETIC, and the failure mode inverted when the graph
// collapsed. With a graph per language, a mis-scoped read returned NOTHING and
// announced itself. With one graph, a mis-scoped read returns EVERY language's
// checks, which looks exactly like success until a Go scan starts reporting
// Python findings. That is why the language-scoping test carries a negative leg.
//
// TWO WHOLE-TYPE READS per graph, both through foundation.FetchNodesByType,
// whose payload carries an explicit limit and an after_id cursor so the drain is
// bounded AND complete. The fixture read is a deliberate whole-type fetch rather
// than a per-check by-id read: one extra bounded drain buys a map that answers
// every fixture lookup without putting a per-check read on an unverified path.
//
// DELIBERATELY NOT foundation.FetchKnowledgeFindings. It supplies no "limit"
// key, and the query compiler stamps the browse default of TEN whenever limit is
// unset — a corpus loader in that shape reads at most ten checks and scans
// vacuously, walking the fixture gate's whole purpose back in through the loader.
func fetchCorpus(ctx context.Context, req foundation.Request, subset []string) (corpusSet, error) {
	langFilter := map[string]string{corpus.MetaLanguage: req.Language}
	checkNodes, err := foundation.FetchNodesByTypeMeta(
		ctx, req.Caller, kgtypes.GraphChecks, checksGraphName, kgtypes.NodeFinding, langFilter)
	if err != nil {
		return corpusSet{}, fmt.Errorf("topology/%s: read checks for %s=%s from the %s graph: %w",
			AnalyzerName, corpus.MetaLanguage, req.Language, kgtypes.GraphChecks, err)
	}
	exampleNodes, err := foundation.FetchNodesByTypeMeta(
		ctx, req.Caller, kgtypes.GraphChecks, checksGraphName, kgtypes.NodeExample, langFilter)
	if err != nil {
		return corpusSet{}, fmt.Errorf("topology/%s: read fixtures for %s=%s from the %s graph: %w",
			AnalyzerName, corpus.MetaLanguage, req.Language, kgtypes.GraphChecks, err)
	}

	set := corpusSet{Fixtures: make(map[string]corpus.Fixture, len(exampleNodes))}
	for _, n := range exampleNodes {
		set.Fixtures[n.GetId()] = corpus.Fixture{ID: n.GetId(), Content: n.GetContent()}
	}
	if err := decodeCheckNodes(&set, checkNodes); err != nil {
		return corpusSet{}, err
	}
	if err := applyChecksSubset(&set, subset); err != nil {
		return corpusSet{}, err
	}

	// Sort explicitly. FetchNodesByType documents id-ascending order because
	// after_id presence pins it, but sorting here keeps finding order
	// deterministic across backends and the render diffable run to run.
	sort.Slice(set.Checks, func(i, j int) bool { return set.Checks[i].Check.ID < set.Checks[j].Check.ID })
	sort.Slice(set.LLMOnly, func(i, j int) bool { return set.LLMOnly[i].ID < set.LLMOnly[j].ID })
	return set, nil
}

// decodeCheckNodes runs every fetched finding node through corpus.ParseCheck and
// files it under the contract's return table.
//
// THE ORDER OF THE BRANCHES IS LOAD-BEARING: error first, then isCheck, then
// LLMOnly, then skip. A reader that writes `if !isCheck { continue }` merges
// rows 2 and 3 and silently destroys the accepted-llm_only lane — a collapse no
// source grep distinguishes from correct code, which is why the lane has its own
// behavioral test rather than a grep gate.
//
// ROW 3 IS THE ONLY SANCTIONED SILENT SKIP. Its population changed when checks
// moved to their own graph: PROSE guidance and MODEL entries stay in
// practice/<language> and no longer reach this reader at all, so the skip is no
// longer the path they take — it now catches a finding IN THE CHECKS GRAPH
// carrying neither check_type nor llm_only, which is an incompletely-authored
// node rather than a different kind of corpus content.
//
// The branch is kept because that state is reachable, not because a known
// population relies on it. It stays keyed on the ABSENCE of check_type and never
// on any model value, so the model vocabulary's growth still cannot reach this
// reader — now for the stronger reason that models are in a different graph.
func decodeCheckNodes(set *corpusSet, nodes []*knowledgev1.Node) error {
	for _, n := range nodes {
		c, isCheck, err := corpus.ParseCheck(n)
		if err != nil {
			// Row 4. The contract is malformed or incomplete; absent fixture ids
			// land here rather than in the fixture gate. Relay verbatim.
			return fmt.Errorf("topology/%s: check %q: %w", AnalyzerName, n.GetId(), err)
		}
		switch {
		case isCheck:
			set.Checks = append(set.Checks, corpusEntry{Check: c, Node: n})
		case c.LLMOnly:
			set.LLMOnly = append(set.LLMOnly, c)
		default:
			continue
		}
	}
	return nil
}

// applyChecksSubset narrows the executable checks to the named ids, HERE, where
// the corpus is known.
//
// An id matching no fetched check is an ERROR naming the id, never a silent
// narrowing — and the subset must actually narrow what EXECUTES: resolving every
// id and then running the whole corpus anyway would satisfy both error paths
// while defeating the param entirely.
func applyChecksSubset(set *corpusSet, subset []string) error {
	if len(subset) == 0 {
		return nil
	}
	want := make(map[string]bool, len(subset))
	for _, id := range subset {
		want[id] = true
	}
	kept := make([]corpusEntry, 0, len(subset))
	for _, e := range set.Checks {
		if want[e.Check.ID] {
			kept = append(kept, e)
			delete(want, e.Check.ID)
		}
	}
	if len(want) > 0 {
		missing := make([]string, 0, len(want))
		for id := range want {
			missing = append(missing, id)
		}
		sort.Strings(missing)
		return fmt.Errorf("topology/%s: %s names %d id(s) matching no check in this corpus: %s",
			AnalyzerName, ExtraKeyChecks, len(missing), strings.Join(missing, ", "))
	}
	set.Checks = kept
	return nil
}
