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
// the target code graph.
func executeGraphCheck(ctx context.Context, req foundation.Request, entry corpusEntry) ([]foundation.Finding, error) {
	c := entry.Check
	sev, err := checkSeverity(c)
	if err != nil {
		return nil, err
	}
	a, err := parseAssertion(c)
	if err != nil {
		return nil, err
	}
	nodes, edges, err := fetchGraphFacts(ctx, req, c, a)
	if err != nil {
		return nil, err
	}
	sites := make([]foundation.Finding, 0)
	for _, v := range evaluate(a, nodes, edges) {
		sites = append(sites, graphSiteFinding(entry, sev, v))
	}
	sortSites(sites)
	return sites, nil
}

// fetchGraphFacts reads the candidate nodes and their edges over the wire.
//
// AN EMPTY CANDIDATE SET IS AN ERROR, NOT A CLEAN RESULT. A graph check needs a
// COLLECTED code graph, which an ast check does not — ast reads the working tree
// off disk. Without this control a repo that was never collected, or an assertion
// naming a node type this graph does not carry, reports zero violations and is
// indistinguishable from a repo with no problems. This is the scan-side twin of
// the fixture-side non-empty-facts control in ValidateGraphFixtures.
func fetchGraphFacts(ctx context.Context, req foundation.Request, c corpus.Check, a graphAssertion) ([]*knowledgev1.Node, []*knowledgev1.Edge, error) {
	nodes, err := foundation.FetchNodesByType(ctx, req.Caller, req.Graph, req.Name, kgtypes.NodeType(a.NodeType))
	if err != nil {
		return nil, nil, fmt.Errorf("topology/%s: check %q: read %s nodes from %s: %w", AnalyzerName, c.ID, a.NodeType, req.Name, err)
	}
	nodes = filterByPathPrefix(nodes, req.PathPrefix)
	if len(nodes) == 0 {
		return nil, nil, fmt.Errorf("topology/%s: check %q found no %q nodes in code graph %q%s — a graph check needs a collected code graph, so run a collect for this repo rather than reading this as a clean scan",
			AnalyzerName, c.ID, a.NodeType, req.Name, pathPrefixClause(req.PathPrefix))
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
		return nil, nil, fmt.Errorf("topology/%s: check %q: read %s edges from %s: %w", AnalyzerName, c.ID, a.EdgeType, req.Name, err)
	}
	return nodes, adaptEdges(edges), nil
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

// filterByPathPrefix narrows the candidate set to nodes under a repo-relative
// subtree. Code-graph node ids are receiver-qualified paths of the form
// path/file.go:Type.Method, so a prefix match is meaningful — but it matches at
// PATH-SEGMENT boundaries so "a/b" never admits the sibling "a/bc", mirroring
// ast's own PackagePrefixes semantics.
func filterByPathPrefix(nodes []*knowledgev1.Node, prefix string) []*knowledgev1.Node {
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

// pathPrefixClause names the narrowing in an error message when one is in force,
// so an empty result under a prefix is never mistaken for an empty graph.
func pathPrefixClause(prefix string) string {
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
