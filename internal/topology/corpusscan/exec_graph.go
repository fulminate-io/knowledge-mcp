// SPDX-License-Identifier: Apache-2.0

// exec_graph.go executes the two graph-shaped check types against a collected
// code graph, and validates their fixtures against a graph built from the
// fixture snippet itself.
//
// ONE EVALUATOR, TWO PRODUCERS. evaluate() (assertion.go) is pure over a
// node/edge set. The SCAN producer is the foundation wire fetchers; the
// VALIDATION producer is parser.Populate over a materialized fixture directory.
// Both reach the same evaluator, which is what makes a passing fixture evidence
// about the scan rather than about a second implementation.
//
// THE PER-LANGUAGE FIDELITY LIMIT that constrains which languages admit these
// checks is family-scoped and lives in doc.go, where a reader looks for scoping.

package corpusscan

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/parser"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
	"github.com/fulminate-io/knowledge-mcp/internal/corpus"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

// executeGraphCheck runs one graph_assertion or topology_threshold check against
// the target code graph, returning its sites and any LEAD finding it produced.
//
// THE SECOND RETURN IS HOW A NOT-RUN CHECK STAYS VISIBLE. THREE narrowings can
// legitimately empty this arm's candidate set, and naming only the first was
// what made this comment wrong: the caller's FILE LIST, the caller's
// PATH_PREFIX, and the CHECK'S OWN declared style_scope_paths. A check that
// evaluated nothing has established nothing about the files in question, so it
// is recorded rather than folded into a clean verdict — and the record names
// WHICH of the three did it, because a caller widens their call and a corpus
// author re-scopes their check.
//
// THE THIRD, THE REPO SCOPE, NEVER REACHES HERE. A check naming another
// repository is skipped before any executor runs, so this arm sees only the two
// path-shaped narrowings and the check's own.
//
// A PATH_PREFIX THAT EMPTIES IT KEEPS THE ERROR rather than becoming a
// disclosure, and that asymmetry is the landed behavior this change did not
// touch: a prefix reaching no candidate is indistinguishable from an uncollected
// graph, while a file list is a caller naming artifacts they already know exist.
func executeGraphCheck(ctx context.Context, req foundation.Request, entry corpusEntry, opts scanOptions, dec checkScopeDecision) ([]foundation.Finding, []foundation.Finding, error) {
	c := entry.Check
	sev, err := checkSeverity(c)
	if err != nil {
		return nil, nil, err
	}
	a, err := parseAssertion(c)
	if err != nil {
		return nil, nil, err
	}
	nodes, edges, emptied, err := fetchGraphFacts(ctx, req, c, a, opts, dec)
	if err != nil {
		return nil, nil, err
	}
	// WHICH NARROWING EMPTIED IT IS WHAT THE READER NEEDS, and it is MEASURED
	// rather than guessed from which narrowings were in force. A caller's file
	// list and a check's own declared paths both legitimately leave a graph check
	// with no candidate, and the remedies are opposite — widen the call, or
	// re-scope the check. Reading "the check carried a scope" as "the check's
	// scope did it" blames the corpus for a caller's narrowing whenever both are
	// in force, which is the common case this arm exists for.
	switch emptied {
	case emptiedByCheckScope:
		return nil, []foundation.Finding{
			outOfScopeDisclosure(c.ID, dec, reasonNoGraphCandidate(a.NodeType))}, nil
	case emptiedByCallerScope:
		return nil, []foundation.Finding{graphNotRunDisclosure(c.ID, a.NodeType, opts.files)}, nil
	case emptiedByNothing:
	}
	sites := make([]foundation.Finding, 0)
	for _, v := range evaluate(a, nodes, edges) {
		sites = append(sites, graphSiteFinding(entry, sev, v))
	}
	sortSites(sites)
	return sites, nil, nil
}

// fetchGraphFacts reads the candidate nodes and their edges over the wire. The
// bool reports that the check did NOT run because the scope narrowed its
// candidates away — never that it ran and found nothing.
//
// AN EMPTY CANDIDATE SET IS AN ERROR, NOT A CLEAN RESULT. A graph check needs a
// COLLECTED code graph, which an ast check does not — ast reads the working tree
// off disk. Without this control a repo that was never collected, or an assertion
// naming a node type this graph does not carry, reports zero violations and is
// indistinguishable from a repo with no problems. This is the scan-side twin of
// the fixture-side non-empty-facts control in ValidateGraphFixtures.
//
// THE CONTROL HAD TO LEARN ONE DIFFERENCE, and the file-list scope is what made
// it necessary. Under a ten-file scope a legitimately-narrowed graph check will
// routinely have zero candidates — that is the scope working, not a missing
// graph — so the two are told apart by whether the check had candidates BEFORE
// the narrowing. Non-empty before and empty after is a NOT-RUN disclosure; empty
// before keeps the error, because that is still an uncollected graph or a node
// type this graph does not carry.
func fetchGraphFacts(ctx context.Context, req foundation.Request, c corpus.Check, a graphAssertion, opts scanOptions, dec checkScopeDecision) ([]*knowledgev1.Node, []*knowledgev1.Edge, scopeEmptier, error) {
	nodes, err := foundation.FetchNodesByType(ctx, req.Caller, req.Graph, req.Name, kgtypes.NodeType(a.NodeType))
	if err != nil {
		return nil, nil, emptiedByNothing, fmt.Errorf("topology/%s: check %q: read %s nodes from %s: %w", AnalyzerName, c.ID, a.NodeType, req.Name, err)
	}
	collected := len(nodes)
	nodes = filterByPathPrefix(nodes, req.PathPrefix, opts.files)
	// THE COUNT IS TAKEN BETWEEN THE TWO FILTERS, and that is the whole of how
	// the two narrowings are told apart afterwards. Both can empty the set and
	// their remedies are opposite, so the arm that reports it has to know WHICH,
	// and after both have run there is nothing left to read it off.
	afterCaller := len(nodes)
	// THE CHECK'S OWN PATHS NARROW THIS ARM TOO. A scoped check that kept every
	// candidate node here while its ast siblings walked a subtree would report
	// sites in files its author declared it does not govern — the requirement is
	// about the check, not about one executor. It is a SECOND conjunct rather
	// than a replacement so the caller's channel keeps its own semantics, and it
	// reads through the walk's own segment-boundary predicate.
	nodes = filterByCheckScope(nodes, dec)
	if len(nodes) == 0 {
		if afterCaller > 0 {
			return nil, nil, emptiedByCheckScope, nil
		}
		if opts.files != nil && collected > 0 {
			return nil, nil, emptiedByCallerScope, nil
		}
		return nil, nil, emptiedByNothing, fmt.Errorf("topology/%s: check %q found no %q nodes in code graph %q%s — a graph check needs a collected code graph, so run a collect for this repo rather than reading this as a clean scan",
			AnalyzerName, c.ID, a.NodeType, req.Name, scopeClause(req.PathPrefix, opts.files))
	}
	ids := make([]string, 0, len(nodes))
	for _, n := range nodes {
		ids = append(ids, n.GetId())
	}
	// FetchEdges is the N+1 guard: it chunks the id set into bounded pivot pages
	// and band-splits a saturated pivot rather than aborting, so this is one
	// bulk read and never a per-node traverse. Its unsplittable-pivot error is
	// PROPAGATED rather than degraded — a silently short edge set yields
	// confidently wrong output that reads exactly like right output.
	edges, err := foundation.FetchEdges(ctx, req.Caller, req.Graph, req.Name, ids, []kgtypes.EdgeType{kgtypes.EdgeType(a.EdgeType)})
	if err != nil {
		return nil, nil, emptiedByNothing, fmt.Errorf("topology/%s: check %q: read %s edges from %s: %w", AnalyzerName, c.ID, a.EdgeType, req.Name, err)
	}
	return nodes, adaptEdges(edges), emptiedByNothing, nil
}

// adaptEdges converts foundation's VALUE edge slice to the pointer slice the
// evaluator takes, which is the shape parser.Populate already returns.
//
// NEVER `for _, e := range` over the value slice: knowledgev1.Edge value-embeds
// a proto noCopy, so ranging by value is a go vet copylocks violation. The
// index-and-address idiom below is the in-tree solution to the identical problem
// in the exposure family's reader.
func adaptEdges(edges []knowledgev1.Edge) []*knowledgev1.Edge {
	out := make([]*knowledgev1.Edge, 0, len(edges))
	for i := range edges {
		out = append(out, &edges[i])
	}
	return out
}

// filterByPathPrefix narrows the candidate set to the run's scope: a
// repo-relative subtree, or the named file set. Code-graph node ids are
// receiver-qualified paths of the form path/file.go:Type.Method, so a path match
// is meaningful — but it matches at PATH-SEGMENT boundaries so "a/b" never
// admits the sibling "a/bc", mirroring ast's own PackagePrefixes semantics.
//
// THE FILE-LIST ARM EXISTS BECAUSE A FILES SCOPE LEAVES THE PREFIX EMPTY BY
// CONSTRUCTION, and an empty prefix here means every candidate. Without this
// arm a graph check under a ten-file scope would evaluate the whole code graph
// and could flag a site in a file the caller never named — the mirror image of
// the silent drop this scope closes, and the same contradiction of "scans only
// those files".
//
// IT DELEGATES BOTH THE FILE PART AND THE PREDICATE rather than re-deriving
// either: nodeFilePath already takes the path off a node id for the finding's
// own file metadata, and parser.MatchesPathPrefixes is the predicate the ast
// walk narrows by, so the two arms of one scope cannot disagree about what a
// path list means.
func filterByPathPrefix(nodes []*knowledgev1.Node, prefix string, files *fileScope) []*knowledgev1.Node {
	if files != nil {
		out := make([]*knowledgev1.Node, 0, len(nodes))
		for _, n := range nodes {
			if parser.MatchesPathPrefixes(nodeFilePath(n.GetId()), files.named) {
				out = append(out, n)
			}
		}
		return out
	}
	prefix = strings.TrimSuffix(strings.TrimSpace(prefix), "/")
	if prefix == "" {
		return nodes
	}
	out := make([]*knowledgev1.Node, 0, len(nodes))
	for _, n := range nodes {
		id := n.GetId()
		if id == prefix || strings.HasPrefix(id, prefix+"/") {
			out = append(out, n)
		}
	}
	return out
}

// scopeEmptier names which narrowing left a graph check with no candidate node.
//
// IT IS A THREE-VALUED ANSWER AND NOT A BOOL because the two non-empty answers
// produce OPPOSITE advice: a caller widens their call, a corpus author re-scopes
// their check. The retired bool said only "something narrowed it away", and the
// arm that read it inferred the cause from which narrowings were in force —
// which named the check whenever a check carried any scope at all, including
// every run where the caller's own file list was what emptied the set.
type scopeEmptier int

const (
	// emptiedByNothing means the candidate set survived, or was empty before any
	// narrowing ran — the second of which is the uncollected-graph error and
	// never a disclosure.
	emptiedByNothing scopeEmptier = iota
	// emptiedByCallerScope means the caller's file list took the last candidate.
	emptiedByCallerScope
	// emptiedByCheckScope means the check's own declared paths did.
	emptiedByCheckScope
)

// filterByCheckScope drops the candidate nodes outside a check's own declared
// paths, through the same predicate the ast walk and the practice-side index
// narrow by, so one scope means one thing across all three.
//
// A CHECK WITH NO PATH SCOPE IS UNTOUCHED, and the guard is on the DECLARATION
// rather than on the effective prefixes: dec.prefixes carries the caller's
// channel too, which filterByPathPrefix has already applied with its own
// semantics, and applying it a second time here would be a second answer to a
// question that already has one.
func filterByCheckScope(nodes []*knowledgev1.Node, dec checkScopeDecision) []*knowledgev1.Node {
	if !dec.scope.PathsSet {
		return nodes
	}
	out := make([]*knowledgev1.Node, 0, len(nodes))
	for _, n := range nodes {
		if parser.MatchesPathPrefixes(nodeFilePath(n.GetId()), dec.scope.Paths) {
			out = append(out, n)
		}
	}
	return out
}

// scopeClause names the narrowing in an error message when one is in force, so
// an empty result under a scope is never mistaken for an empty graph. It names
// whichever of the two channels the caller used; they are mutually exclusive.
func scopeClause(prefix string, files *fileScope) string {
	if files != nil {
		return fmt.Sprintf(" under the %d named file(s)", len(files.named))
	}
	if strings.TrimSpace(prefix) == "" {
		return ""
	}
	return fmt.Sprintf(" under path_prefix %q", prefix)
}

// graphSiteFinding builds one finding for one violating node.
//
// A code-graph node id yields a FILE but never a LINE, so the line key is OMITTED
// ENTIRELY rather than written as zero: an absent key is an honest "this finding
// is file-granular", where a zero would be a false row in any join over it. The
// natural instinct is to fill every declared key; do not.
func graphSiteFinding(entry corpusEntry, sev foundation.Severity, v violation) foundation.Finding {
	return foundation.Finding{
		Algorithm: AnalyzerName,
		Severity:  sev,
		Title:     checkTitle(entry.Node, entry.Check.ID) + " at " + v.NodeID,
		Summary:   strings.TrimSpace(v.NodeID + " " + v.Reason + ". " + checkGuidance(entry.Node)),
		Evidence:  []string{v.NodeID, entry.Check.ID},
		Metrics:   map[string]float64{"degree": float64(v.Degree)},
		Metadata: map[string]string{
			MetaKeyFile:    nodeFilePath(v.NodeID),
			MetaKeyCheckID: entry.Check.ID,
		},
	}
}

// nodeFilePath takes the path component off a code-graph node id, which is
// path/file.go:Symbol. An id carrying no symbol suffix is already a path.
func nodeFilePath(id string) string {
	if i := strings.LastIndex(id, ":"); i > 0 {
		return id[:i]
	}
	return id
}

// ValidateGraphFixtures proves a graph-shaped check FIRES on its bad example and
// stays SILENT on its good one, by turning each fixture snippet into real graph
// facts and running the SAME evaluator the scan uses.
//
// It wraps the CONTRACT's sentinels — corpus.ErrFixtureValidation and
// corpus.ErrFixtureMaterialization — and declares no taxonomy of its own, so a
// caller classifies both validators identically with errors.Is and never parses
// a message. That is what keeps this package's boundary with its callers at two
// exported symbols.
func ValidateGraphFixtures(ctx context.Context, c corpus.Check, bad, good corpus.Fixture) error {
	a, err := parseAssertion(c)
	if err != nil {
		return fmt.Errorf("%v: %w", err, corpus.ErrFixtureValidation)
	}
	badNodes, badEdges, err := materializeFixture(ctx, c, a, bad)
	if err != nil {
		return err
	}
	goodNodes, goodEdges, err := materializeFixture(ctx, c, a, good)
	if err != nil {
		return err
	}
	badV := evaluate(a, badNodes, badEdges)
	goodV := evaluate(a, goodNodes, goodEdges)
	if len(badV) == 0 {
		return fmt.Errorf("corpus: the check is SILENT on its bad example %q (bad violated %d, good violated %d): %w",
			bad.ID, len(badV), len(goodV), corpus.ErrFixtureValidation)
	}
	if len(goodV) != 0 {
		return fmt.Errorf("corpus: the check FIRES on its good example %q (bad violated %d, good violated %d): %w",
			good.ID, len(badV), len(goodV), corpus.ErrFixtureValidation)
	}
	return nil
}

// materializeFixture writes one fixture's Content to a temp directory and turns
// it into graph facts, applying THREE controls in order.
//
// FIRE-THEN-SILENT MEANS NOTHING UNTIL THE FIXTURE ACTUALLY PRODUCED FACTS. A
// collect that yielded no nodes is "silent on good" for every check ever
// written, which is exactly the vacuous admission the fixture gate exists to
// end — and it matters MORE on the good fixture, where an empty result passes
// for the wrong reason instead of failing.
func materializeFixture(ctx context.Context, c corpus.Check, a graphAssertion, f corpus.Fixture) ([]*knowledgev1.Node, []*knowledgev1.Edge, error) {
	name, ok := treesitter.FixtureFileName(c.Language)
	if !ok {
		return nil, nil, fmt.Errorf("corpus: no fixture filename for %s=%q: %w", corpus.MetaLanguage, c.Language, corpus.ErrFixtureMaterialization)
	}
	dir, err := os.MkdirTemp("", "corpusscan-fixture-*")
	if err != nil {
		return nil, nil, fmt.Errorf("corpus: temp directory for fixture %q: %v: %w", f.ID, err, corpus.ErrFixtureMaterialization)
	}
	defer os.RemoveAll(dir)
	if err := os.WriteFile(filepath.Join(dir, name), []byte(f.Content), 0o600); err != nil {
		return nil, nil, fmt.Errorf("corpus: write fixture %q: %v: %w", f.ID, err, corpus.ErrFixtureMaterialization)
	}

	// CONTROL 1 — DISCOVERY SAW EXACTLY THE FILE WE WROTE. The empty
	// DiscoveryOptions is deliberate: parser.Populate takes no options and
	// builds its own zero-valued set, so any other value here would measure a
	// DIFFERENT walk than the one Populate runs and could certify a file
	// Populate never sees. A consequence worth stating so nobody debugs it
	// twice: Populate does NOT lift exclusions, so a fixture over the size cap
	// or on an excluded path is dropped before parsing — this control turns that
	// from a silent clean into a named error.
	files, _, err := parser.DiscoverFilesReporting(ctx, dir, parser.DiscoveryOptions{})
	if err != nil {
		return nil, nil, fmt.Errorf("corpus: discover fixture %q: %v: %w", f.ID, err, corpus.ErrFixtureMaterialization)
	}
	if len(files) != 1 {
		return nil, nil, fmt.Errorf("corpus: fixture %q was written but discovery handed the walk %d file(s), not 1: %w", f.ID, len(files), corpus.ErrFixtureMaterialization)
	}

	res, err := parser.Populate(ctx, "corpusscan-fixture", dir)
	if err != nil {
		return nil, nil, fmt.Errorf("corpus: collect fixture %q: %v: %w", f.ID, err, corpus.ErrFixtureMaterialization)
	}

	// CONTROL 2 — CHUNKING LOST NOTHING. SUM THE VALUES: the report seeds a zero
	// entry per reason, so it is never an empty map and a len() test could never
	// fire.
	dropped := 0
	for _, n := range res.ChunkReport.DroppedByReason {
		dropped += n
	}
	if dropped > 0 {
		return nil, nil, fmt.Errorf("corpus: fixture %q lost %d file(s) during chunking, so its facts are incomplete: %w", f.ID, dropped, corpus.ErrFixtureMaterialization)
	}

	// CONTROL 3 — THE SNIPPET PRODUCED FACTS THE ASSERTION CAN BIND TO. With
	// controls 1 and 2 green, discovery DID hand the file over and chunking DID
	// succeed, so an empty result is genuinely about the CONTENT: an author wrote
	// a snippet with nothing for this assertion to bind to. That attribution is
	// EARNED by the two controls above rather than guessed, which is why this one
	// classifies as a validation failure rather than an environment fault.
	//
	// THE COUNT THAT MATTERS IS PER-NODE-TYPE, NOT TOTAL NODES, AND THIS WAS
	// MEASURED RATHER THAN REASONED. A bare `package p` snippet still produces a
	// file node and a language hub node, so a total-node-count control reads
	// non-zero for a fixture declaring nothing — it would admit exactly the
	// vacuous check this gate exists to refuse, because "silent on good" is
	// trivially true when the good example holds no candidate node at all.
	candidates := 0
	for _, n := range res.Nodes {
		if n.GetType() == a.NodeType {
			candidates++
		}
	}
	if candidates == 0 {
		return nil, nil, fmt.Errorf("corpus: fixture %q declares no %q node for the check to bind to (it produced %d node(s) of other kinds), so its result proves nothing: %w",
			f.ID, a.NodeType, len(res.Nodes), corpus.ErrFixtureValidation)
	}
	return res.Nodes, res.Edges, nil
}
