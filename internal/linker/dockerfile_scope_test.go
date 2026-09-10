// SPDX-License-Identifier: Apache-2.0

package linker

// dockerfile_scope_test.go — the BREADTH contract of the Dockerfile pass's two
// entry points, and the three things the scoped one carries.
//
// WHY BREADTH NEEDS ITS OWN FILE. LinkDockerfilesInGraph and LinkDockerfiles
// emit byte-identical edges for the graph they share, so an EDGES-ONLY assertion
// passes identically under both and observes nothing about scope. What separates
// them is what they READ: the scoped entry point issues no graph-name
// enumeration at all and touches no graph but the one it was named, while the
// sweep enumerates once and reads every non-overlay code graph. Every assertion
// here is therefore a READ assertion with the edge count beside it as the
// same-run proof that the pass actually ran.

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// scopeRepo is one seeded code graph: a Dockerfile that COPYs one source file,
// plus that source file, so the pass has exactly one BUILDS edge to find in it.
type scopeRepo struct {
	name string
	df   *knowledgev1.Node
	src  *knowledgev1.Node
}

func newScopeRepo(name string) scopeRepo {
	return scopeRepo{
		name: name,
		df: &knowledgev1.Node{
			Id: name + ":Dockerfile", Type: string(kgtypes.NodeFile),
			FilePath: "Dockerfile", Content: "FROM scratch\nCOPY main.go /\n",
		},
		src: &knowledgev1.Node{
			Id: name + ":main.go", Type: string(kgtypes.NodeFile), FilePath: "main.go",
		},
	}
}

// scopeFake wraps the package fake with a per-graph node seed and a per-graph
// READ COUNTER. The counter is the instrument: it is what tells "the pass linked
// A" apart from "the pass linked A and read B and C on the way".
type scopeFake struct {
	*fakeGraphCaller
	repos       map[string]scopeRepo
	readsByRepo map[string]int
	// seq records the ORDER of code-graph reads as "enum" and "read:<repo>".
	//
	// ORDER IS THE INSTRUMENT, NOT A RAW ENUMERATION COUNT, and the difference is
	// a measured one. A graph-name enumeration over the code family is issued by
	// TWO producers on this path: the pass's own breadth resolution, and
	// crossgraph's endpoint locator inside emitLink, which enumerates foreign
	// graphs to resolve a proxy. Counting enumerations alone therefore cannot say
	// which producer made one. The pass's own enumeration is the FIRST code read
	// of the pass, before any node is fetched; the locator's can only happen after
	// an edge has been discovered, which needs a node read first. So "the first
	// code read is a node read" is exactly the claim, and it is falsifiable.
	seq []string
	// enumerations counts RETURN_MODE_GRAPH_NAMES reads of any producer.
	enumerations int
	// overlayNames are extra names the enumeration reports that no repo backs.
	overlayNames []string
}

func newScopeFake(t *testing.T, names ...string) *scopeFake {
	t.Helper()
	f := &scopeFake{
		fakeGraphCaller: &fakeGraphCaller{},
		repos:           map[string]scopeRepo{},
		readsByRepo:     map[string]int{},
	}
	for _, n := range names {
		r := newScopeRepo(n)
		f.repos[n] = r
		// RESIDENCY IS DECLARED PER GRAPH, not family-wide: this fixture holds
		// three code graphs, and a family-wide seed resolves every id in all
		// three — which makes the endpoint locator's first-hit map order and the
		// proxy's graph-name component a coin flip.
		f.seedCodeNodeIn(n, r.df)
		f.seedCodeNodeIn(n, r.src)
	}
	f.respond = func(tool string, args map[string]any) (kgtools.ToolResult, error) {
		if tool != "query" {
			return kgtools.ToolResult{Content: []kgtools.ContentBlock{{Type: "text", Text: `{}`}}}, nil
		}
		if graph, _ := args["graph"].(string); graph != "code" {
			return kgtools.ToolResult{Content: []kgtools.ContentBlock{{Type: "text", Text: `{}`}}}, nil
		}
		typ, hasType := args["type"].(string)
		if !hasType {
			f.enumerations++
			f.seq = append(f.seq, "enum")
			names := make([]string, 0, len(f.repos)+len(f.overlayNames))
			for n := range f.repos {
				names = append(names, n)
			}
			names = append(names, f.overlayNames...)
			return jsonResult(t, map[string]any{"graphs": names}), nil
		}
		repo, _ := args["repo"].(string)
		f.readsByRepo[repo]++
		f.seq = append(f.seq, "read:"+repo)
		r, ok := f.repos[repo]
		if !ok {
			return jsonResult(t, map[string]any{"nodes": []*knowledgev1.Node{}}), nil
		}
		if typ == string(kgtypes.NodeFile) {
			return jsonResult(t, map[string]any{"nodes": []*knowledgev1.Node{r.df, r.src}}), nil
		}
		return jsonResult(t, map[string]any{"nodes": []*knowledgev1.Node{}}), nil
	}
	return f
}

// TestLinkDockerfilesInGraph_ReadsOnlyTheNamedGraph is ARM 1: the post-collect
// tail's breadth.
//
// THE ASSERTION IS TWO-SIDED AND THE SECOND SIDE IS THE ONE THAT OBSERVES THE
// BOUND. (i) the named graph's COPY produces its BUILDS edge, so the pass ran;
// (ii) the other two code graphs are read ZERO times AND no graph-name
// enumeration is issued at all. An edges-only assertion passes identically under
// the unbounded sweep, which is why (ii) is not optional.
func TestLinkDockerfilesInGraph_ReadsOnlyTheNamedGraph(t *testing.T) {
	gc := newScopeFake(t, "repo-a", "repo-b", "repo-c")

	n, err := LinkDockerfilesInGraph(context.Background(), gc, LinkOptions{}, "repo-a")
	require.NoError(t, err)

	// (i) the pass ran against the named graph.
	assert.Equal(t, 1, n, "the named graph's COPY must produce its BUILDS edge")
	require.Len(t, gc.capturedLinks, 1)
	assert.Equal(t, "proxy:repo-a:repo-a:Dockerfile", gc.capturedLinks[0].FromID)

	// (ii) and nothing else was read.
	require.NotEmpty(t, gc.seq, "control: the pass issued code reads at all")
	assert.Equal(t, "read:repo-a", gc.seq[0],
		"a scoped pass knows its graph name already: it opens with a NODE read of that graph, "+
			"never with a code-family enumeration — that enumeration is the read the bound removes")
	assert.Zero(t, gc.readsByRepo["repo-b"], "no read may reach a graph the collect did not touch")
	assert.Zero(t, gc.readsByRepo["repo-c"], "no read may reach a graph the collect did not touch")
	assert.Positive(t, gc.readsByRepo["repo-a"],
		"control: the named graph WAS read, so the two zeros above are a scope, not a dead fake")
}

// TestLinkDockerfilesInGraph_ReadsTheVocabularyOncePerPass is the KILL TEST for
// the withVocabCache wrap the scoped entry point carries.
//
// THE WRAP LIVES IN THE ENTRY POINT, NOT IN THE UNIT. linkDockerfilesInRepo has
// never carried it, so a scoped entry point that called the unit directly would
// compile, emit identical edges and silently trade one Stats read per pass for
// one per emitted edge. Nothing in the captured links can tell those apart; the
// Stats counter is the only observable, which is why this row exists.
func TestLinkDockerfilesInGraph_ReadsTheVocabularyOncePerPass(t *testing.T) {
	gc := newScopeFake(t, "repo-a")
	// Two COPY sources, so the pass emits TWO edges: with the wrap removed the
	// count below goes to two, which is the failure this row names.
	r := gc.repos["repo-a"]
	r.df.Content = "FROM scratch\nCOPY main.go /\nCOPY second.go /\n"
	second := &knowledgev1.Node{Id: "repo-a:second.go", Type: string(kgtypes.NodeFile), FilePath: "second.go"}
	gc.seedCodeNode(second)
	gc.respond = func(tool string, args map[string]any) (kgtools.ToolResult, error) {
		if tool == "query" {
			if graph, _ := args["graph"].(string); graph == "code" {
				if typ, ok := args["type"].(string); ok && typ == string(kgtypes.NodeFile) {
					return jsonResult(t, map[string]any{"nodes": []*knowledgev1.Node{r.df, r.src, second}}), nil
				}
				if _, ok := args["type"]; ok {
					return jsonResult(t, map[string]any{"nodes": []*knowledgev1.Node{}}), nil
				}
			}
		}
		return kgtools.ToolResult{Content: []kgtools.ContentBlock{{Type: "text", Text: `{}`}}}, nil
	}

	n, err := LinkDockerfilesInGraph(context.Background(), gc, LinkOptions{}, "repo-a")
	require.NoError(t, err)
	require.Equal(t, 2, n, "control: the pass emitted TWO edges, so a per-edge read would be visible")
	assert.Equal(t, 1, gc.statsCalls,
		"the vocabulary is read ONCE for the whole pass; without the withVocabCache wrap this is one read per emitted edge")
}

// TestLinkDockerfilesInGraph_EmptyNameErrorsAndReadsNothing is the KILL TEST for
// the no-fallback guard.
//
// THE WRONG ANSWERS ARE BOTH AVAILABLE and both are silent: falling back to the
// enumeration restores the whole cross-graph fan-out the scope exists to remove,
// and returning (0, nil) reports a pass that never ran as a pass that found
// nothing. The error is what makes the caller decide, and the zero reads are
// what prove no sweep happened on the way to it.
func TestLinkDockerfilesInGraph_EmptyNameErrorsAndReadsNothing(t *testing.T) {
	gc := newScopeFake(t, "repo-a", "repo-b")

	n, err := LinkDockerfilesInGraph(context.Background(), gc, LinkOptions{}, "")
	require.Error(t, err, "a scoped pass with no graph name is a wiring defect, not a request to sweep")
	assert.Contains(t, err.Error(), "no all-graphs fallback",
		"the error names the thing it refuses to do")
	assert.Zero(t, n)
	assert.Zero(t, gc.enumerations, "and it must not enumerate on the way to the error")
	assert.Empty(t, gc.readsByRepo, "nor read any graph")
}

// TestLinkDockerfilesInGraph_OverlayNameIsSkipped is the KILL TEST for the "@"
// overlay skip, which also lives in the wrapper rather than in the unit.
//
// A BRANCH OVERLAY IS NOT A REPO. Linking into one writes BUILDS edges keyed on
// an overlay's node ids, which the base graph does not carry; the sweep has
// always skipped them and the scoped entry point must too.
func TestLinkDockerfilesInGraph_OverlayNameIsSkipped(t *testing.T) {
	gc := newScopeFake(t, "repo-a")

	n, err := LinkDockerfilesInGraph(context.Background(), gc, LinkOptions{}, "repo-a@feature-branch")
	require.NoError(t, err, "an overlay is skipped, not refused: it is a legitimate graph, just not this pass's subject")
	assert.Zero(t, n)
	assert.Empty(t, gc.readsByRepo, "an overlay name must reach no read at all")

	// CONTROL, same fake and same call: the base name DOES link, so the zero above
	// is the skip acting rather than a fake that answers nothing.
	n, err = LinkDockerfilesInGraph(context.Background(), gc, LinkOptions{}, "repo-a")
	require.NoError(t, err)
	assert.Equal(t, 1, n, "control: the base graph links")
}

// containsSuffix reports whether any entry ends with suffix.
func containsSuffix(ss []string, suffix string) bool {
	for _, s := range ss {
		if strings.HasSuffix(s, suffix) {
			return true
		}
	}
	return false
}

// firstRead returns the index of the first node read in the recorded sequence.
func firstRead(seq []string) int {
	for i, s := range seq {
		if strings.HasPrefix(s, "read:") {
			return i
		}
	}
	return len(seq) - 1
}

// countSeq counts exact matches in a recorded sequence slice.
func countSeq(seq []string, want string) int {
	n := 0
	for _, s := range seq {
		if s == want {
			n++
		}
	}
	return n
}
