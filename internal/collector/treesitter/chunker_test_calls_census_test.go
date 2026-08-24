// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TEST_CALLS is silently excluded by every consumer keyed on the string
// "CALLS", and that exclusion is the RULED DEFAULT: centrality and every CALLS
// consumer stay safe, and no consumer keyed on EdgeCalls ever touches test
// traffic. THE RULED RISK IS THE OTHER SIDE OF THAT TRADE — an
// un-updated renderer shows NO test coverage rather than wrong coverage, and a
// silent exclusion is indistinguishable from an oversight.
//
// So every exclusion is a DECISION with a stated reason, and this census is
// what stops a new consumer from defaulting into one. It derives its subject
// set FROM THE TREE rather than from a hand-written list: a consumer file that
// appears in the walk with no row fails, a row naming a file that no longer
// consumes CALLS fails too, and a row whose DISPOSITION disagrees with the file
// — censused as opting in while naming no test-call vocabulary, or censused as
// excluded/follow_up while reading TEST_CALLS — fails as well, so the table
// cannot drift into describing a state of the tree that has passed. Same
// closed-allowlist discipline the resolution matrix and the callee-capture
// census already use in this package.
type testCallsDisposition string

const (
	// dispositionOptsIn — reads TEST_CALLS deliberately.
	dispositionOptsIn testCallsDisposition = "opts_in"
	// dispositionExcluded — stays CALLS-only, on purpose, for the stated reason.
	dispositionExcluded testCallsDisposition = "excluded_by_decision"
	// dispositionFollowUp — should opt in, does not yet, with the work named.
	dispositionFollowUp testCallsDisposition = "follow_up"
	// dispositionProducer — not a consumer at all: it declares or emits the
	// vocabulary. Recorded as such rather than having a consumer disposition
	// forced onto it, which would be a decision about nothing.
	dispositionProducer testCallsDisposition = "producer"
)

type testCallsConsumerRow struct {
	// Path is repo-relative and must match a file the walk finds.
	Path        string
	Disposition testCallsDisposition
	// Reason is MANDATORY. An exclusion whose reason is empty is
	// indistinguishable from an oversight, which is the precise failure the
	// ruling flags.
	Reason string
}

// testCallsConsumerCensus carries one row per file under the two internal trees
// that names EdgeCalls or the literal "CALLS".
//
// EVERY REASON BELOW WAS READ IN CURRENT SOURCE, not inherited from the plan.
var testCallsConsumerCensus = []testCallsConsumerRow{
	{
		Path:        "cmd/knowledge/internal/kgtypes/edge_types.go",
		Disposition: dispositionProducer,
		Reason: "Declares the client-side edge vocabulary, EdgeCalls and EdgeTestCalls both. " +
			"It consumes nothing; a disposition here would be a decision about a const block.",
	},
	{
		Path:        "cmd/knowledge/internal/collector/treesitter/types.go",
		Disposition: dispositionProducer,
		Reason: "The chunker's own EdgeType mirror of the kgtypes vocabulary — a deliberate " +
			"per-module duplicate, since no hand-written package is shared across the two binaries.",
	},
	{
		Path:        "cmd/knowledge/internal/collector/treesitter/chunker_edges.go",
		Disposition: dispositionProducer,
		Reason: "extractCallEdges BUILDS the call edges; the Type is stamped by its two callers " +
			"(emitDeclarationEdges and emitTestBlockCallEdges), so this file decides nothing about test traffic.",
	},
	{
		Path:        "cmd/knowledge/internal/topology/graph/blast_radius.go",
		Disposition: dispositionExcluded,
		Reason: "Impact radius walks kgtypes.EdgeCalls (blast_radius.go:217). Test traffic must not " +
			"widen a production symbol's blast radius — a symbol is not more dangerous to change " +
			"because more tests exercise it. parseEdgeTypeOverride already lets a caller pass " +
			"edge_types=TEST_CALLS explicitly when that IS the question being asked.",
	},
	{
		Path:        "cmd/knowledge/internal/topology/graph/god_object_metrics.go",
		Disposition: dispositionExcluded,
		Reason: "Fan-in / fan-out / coupling all filter on kgtypes.EdgeCalls (god_object_metrics.go:68,131,161,213). " +
			"Counting test callers would make a well-tested helper look like a god object, which is " +
			"the arbitrary style-dependent distortion the distinct edge type exists to remove.",
	},
	{
		Path:        "cmd/knowledge-server/internal/store/graph_traversal.go",
		Disposition: dispositionExcluded,
		Reason: "FindCallers/FindCallees are the fixed-CALLS convenience arms (graph_traversal.go:68,73) and " +
			"stay production-only. They are not the general surface: Traverse and FindRelated take " +
			"edge types from the caller, so asking for test callers needs no change here.",
	},
	{
		Path:        "cmd/knowledge-server/internal/store/edge_types_vocab.go",
		Disposition: dispositionExcluded,
		Reason: "CHECKED, AND THE ANSWER IS THAT TEST_CALLS IS ALREADY TRAVERSABLE. The server's const " +
			"block is a vocabulary, not a validator: bootstrap/engine_decode.go:133-134 converts every " +
			"requested edge type with a bare store.EdgeType(et) and applies no allowlist — in contrast " +
			"to fieldPredicateAllowlist directly below it, which does. So no server-side const is " +
			"needed for traverse(edge_types:[\"TEST_CALLS\"]) to work, and adding one to a server " +
			"vocabulary the client never reads would be ceremony.",
	},
	{
		Path:        "cmd/knowledge/internal/tools/intercept_query_analyze_node.go",
		Disposition: dispositionOptsIn,
		Reason: "THE RENDERER RISK, NOW CLOSED. traverseCallNodes takes the call edge type as a " +
			"parameter, and composeAnalyzeNode runs the caller and callee walks TWICE — once over " +
			"kgtypes.EdgeCalls and once over kgtypes.EdgeTestCalls — so a user asking what calls a " +
			"symbol sees its test coverage too. The two edge types get SEPARATE walks and SEPARATE " +
			"carriers (AnalyzeView.TestCallers/TestCallees and their groups), never a merge into the " +
			"production lists: a merge would re-create at the display layer exactly the conflation " +
			"the distinct edge type was introduced to end, and a single mixed-edge-type walk would " +
			"do the same inside the group machinery, where the frontier short-circuit reads the whole " +
			"edge slice. An empty test side renders NO section rather than a zero count, because " +
			"until a repo is re-collected no TEST_CALLS edge exists and a none-line would report an " +
			"absence as a fact.",
	},
}

// TestTestCallsConsumerCensus walks the two internal trees, finds every file
// that consumes the CALLS edge type, and requires each to carry a disposition.
func TestTestCallsConsumerCensus(t *testing.T) {
	root := repoRootForCensus(t)

	found := map[string]bool{}
	// readsTestCalls records, per subject file, whether it names the test-call
	// vocabulary at all. It is what turns each row's disposition from a claim
	// into an assertion — see the disposition-accuracy check below.
	readsTestCalls := map[string]bool{}
	for _, tree := range []string{
		filepath.Join("cmd", "knowledge", "internal"),
		filepath.Join("cmd", "knowledge-server", "internal"),
	} {
		walkRoot := filepath.Join(root, tree)
		require.DirExists(t, walkRoot, "census control: the consumer tree exists")
		err := filepath.WalkDir(walkRoot, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") {
				return nil
			}
			// TEST files are excluded from the subject set. A test asserting on
			// CALLS is not a consumer whose behavior the ruling governs — it is
			// an assertion about one, and requiring a disposition for each of the
			// ~80 of them would bury the nine real decisions in bookkeeping.
			if strings.HasSuffix(path, "_test.go") {
				return nil
			}
			body, readErr := os.ReadFile(path) //nolint:gosec // walks this repo's own source tree
			if readErr != nil {
				return readErr
			}
			// The surface: a reference to the constant, or to the wire literal.
			// "EdgeTestCalls" does not contain "EdgeCalls", and "TEST_CALLS" does
			// not contain the quoted "CALLS", so this ticket's own additions do
			// not enlarge the subject set by accident.
			if !strings.Contains(string(body), "EdgeCalls") && !strings.Contains(string(body), `"CALLS"`) {
				return nil
			}
			rel, relErr := filepath.Rel(root, path)
			if relErr != nil {
				return relErr
			}
			slashed := filepath.ToSlash(rel)
			found[slashed] = true
			readsTestCalls[slashed] = strings.Contains(string(body), "EdgeTestCalls") ||
				strings.Contains(string(body), `"TEST_CALLS"`)
			return nil
		})
		require.NoError(t, err)
	}

	// KNOWN-POSITIVE CONTROL. Every assertion below is about set agreement, and
	// two sets that both went empty agree perfectly. A walk that matched nothing
	// — a moved tree, a broken predicate, a filter that swallowed every file —
	// must fail loudly here rather than pass as a clean census.
	require.NotEmpty(t, found, "census control: the walk found at least one CALLS consumer")
	require.NotEmpty(t, testCallsConsumerCensus, "census control: the disposition table is not empty")

	byPath := map[string]testCallsConsumerRow{}
	for _, row := range testCallsConsumerCensus {
		require.NotContains(t, byPath, row.Path, "duplicate census row for %s", row.Path)
		byPath[row.Path] = row
	}

	for path := range found {
		row, ok := byPath[path]
		if !assert.True(t, ok,
			"%s consumes the CALLS edge type and carries NO TEST_CALLS disposition. "+
				"Add a row to testCallsConsumerCensus stating opts_in, excluded_by_decision or "+
				"follow_up, with the reason. A silent exclusion is indistinguishable from an oversight.",
			path) {
			continue
		}
		assert.NotEmpty(t, strings.TrimSpace(row.Reason),
			"%s carries a disposition with no reason", path)
		// DISPOSITION ACCURACY. A row that merely EXISTS proves only that someone
		// once wrote a sentence; it does not stay true as the file changes. A
		// consumer that opts in and is later reverted, or one that starts reading
		// test traffic while its row still says follow_up, is the same silent
		// exclusion in the other direction — and the row is the only place a
		// reader looks. So each disposition is checked against whether the file
		// actually names the test-call vocabulary. Producers are exempt: they
		// DECLARE that vocabulary, which says nothing about consuming it.
		switch row.Disposition {
		case dispositionOptsIn:
			assert.True(t, readsTestCalls[path],
				"%s is censused as opts_in but names neither EdgeTestCalls nor \"TEST_CALLS\". "+
					"Either it never opted in, or the opt-in was reverted and the row was not.", path)
		case dispositionExcluded, dispositionFollowUp:
			assert.False(t, readsTestCalls[path],
				"%s names the test-call vocabulary but is censused as %q. A file that reads "+
					"TEST_CALLS has opted in; move the row to opts_in with the reason.", path, row.Disposition)
		case dispositionProducer:
		default:
			assert.Failf(t, "unknown disposition",
				"%s carries disposition %q, which is not one of the four", path, row.Disposition)
		}
	}

	for path := range byPath {
		assert.True(t, found[path],
			"the census carries a row for %s, which no longer consumes the CALLS edge type. "+
				"Remove the row, or restore the consumer.", path)
	}
}

// repoRootForCensus walks up from the package directory to the tree holding
// BOTH internal trees the census covers.
//
// It anchors on those two directories rather than on go.mod, because go.mod is
// ambiguous here: cmd/knowledge is its own module, so the first go.mod above
// this package is the CLIENT module's and stops the walk one binary short of
// the server tree the census must also read.
func repoRootForCensus(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	require.NoError(t, err)
	for {
		_, clientErr := os.Stat(filepath.Join(dir, "cmd", "knowledge", "internal"))
		_, serverErr := os.Stat(filepath.Join(dir, "cmd", "knowledge-server", "internal"))
		if clientErr == nil && serverErr == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		require.NotEqual(t, parent, dir,
			"walked to the filesystem root without finding a tree holding both cmd/knowledge/internal and cmd/knowledge-server/internal")
		dir = parent
	}
}
