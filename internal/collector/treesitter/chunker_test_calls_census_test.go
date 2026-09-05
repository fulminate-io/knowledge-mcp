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
	// Path is relative to the root the half that declares the row walks:
	// MODULE-relative here (internal/...), REPO-relative in the staging-only
	// server half (cmd/knowledge-server/internal/...). Either way it must match
	// a file that half's walk finds.
	Path        string
	Disposition testCallsDisposition
	// Reason is MANDATORY. An exclusion whose reason is empty is
	// indistinguishable from an oversight, which is the precise failure the
	// ruling flags.
	Reason string
}

// testCallsConsumerCensus carries one row per file under THIS MODULE's internal
// tree that names EdgeCalls or the literal "CALLS". Paths are MODULE-relative,
// which is the one spelling correct in both layouts this file is compiled in:
// the tree is cmd/knowledge/internal here and internal/ in the published
// mirror, and the walk takes each path relative to the module root in both.
//
// THE SERVER MODULE'S CONSUMERS ARE CENSUSED BY THE OTHER HALF,
// chunker_test_calls_census_server_test.go, which the sync script removes from
// the published tree because the mirror has no server module to walk. The two
// halves together cover exactly what the single two-tree census covered, and
// each asserts set agreement in BOTH directions over its own tree.
//
// EVERY REASON BELOW WAS READ IN CURRENT SOURCE, not inherited from the plan.
var testCallsConsumerCensus = []testCallsConsumerRow{
	{
		Path:        "internal/kgtypes/edge_types.go",
		Disposition: dispositionProducer,
		Reason: "Declares the client-side edge vocabulary, EdgeCalls and EdgeTestCalls both. " +
			"It consumes nothing; a disposition here would be a decision about a const block.",
	},
	{
		Path:        "internal/kgtypes/edge_types_cloud.go",
		Disposition: dispositionProducer,
		Reason: "Declares the cloud, CI/CD, cross-domain and log-graph edge vocabulary, split out " +
			"of edge_types.go by vocabulary. It names EdgeCalls in ONE PLACE ONLY — the " +
			"EdgeCorrelatesWith doc comment, which cites it as an example of the structural " +
			"confirmation a log correlation requires — so it consumes nothing and a disposition " +
			"here would be a decision about a const block.",
	},
	{
		Path:        "internal/collector/treesitter/types.go",
		Disposition: dispositionProducer,
		Reason: "The chunker's own EdgeType mirror of the kgtypes vocabulary — a deliberate " +
			"per-module duplicate, since no hand-written package is shared across the two binaries.",
	},
	{
		Path:        "internal/collector/treesitter/chunker_edges.go",
		Disposition: dispositionProducer,
		Reason: "extractCallEdges BUILDS the call edges; the Type is stamped by its two callers " +
			"(emitDeclarationEdges and emitTestBlockCallEdges), so this file decides nothing about test traffic.",
	},
	{
		Path:        "internal/topology/graph/blast_radius.go",
		Disposition: dispositionExcluded,
		Reason: "Impact radius walks kgtypes.EdgeCalls (blast_radius.go:217). Test traffic must not " +
			"widen a production symbol's blast radius — a symbol is not more dangerous to change " +
			"because more tests exercise it. parseEdgeTypeOverride already lets a caller pass " +
			"edge_types=TEST_CALLS explicitly when that IS the question being asked.",
	},
	{
		Path:        "internal/topology/corpusscan/assertion.go",
		Disposition: dispositionOptsIn,
		Reason: "codeEdgeTypes admits kgtypes.EdgeTestCalls alongside kgtypes.EdgeCalls, so a corpus " +
			"author asks for test traffic BY NAME in the check body's edge_type and gets exactly what " +
			"they named — no check silently mixes the two, because the evaluator counts one edge type " +
			"per assertion. The allowlist exists to refuse a typo'd edge type before the read rather " +
			"than to narrow the vocabulary: an unadmitted spelling would otherwise return zero edges " +
			"and read as a clean scan.",
	},
	{
		Path:        "internal/topology/graph/god_object_metrics.go",
		Disposition: dispositionExcluded,
		Reason: "Fan-in / fan-out / coupling all filter on kgtypes.EdgeCalls (god_object_metrics.go:68,131,161,213). " +
			"Counting test callers would make a well-tested helper look like a god object, which is " +
			"the arbitrary style-dependent distortion the distinct edge type exists to remove.",
	},
	{
		Path:        "internal/tools/intercept_query_analyze_node.go",
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

// TestTestCallsConsumerCensus walks THIS MODULE's internal tree, finds every
// file that consumes the CALLS edge type, and requires each to carry a
// disposition.
//
// THE ROOT IS THE MODULE ROOT, not the repository root, and that is what makes
// this half publishable. repoRoot walks to the first go.mod: cmd/knowledge in
// this repository, the mirror root in the published one. The walk root is
// <module>/internal in both, and every path below is taken relative to the
// module root, so one set of rows is correct in both layouts.
func TestTestCallsConsumerCensus(t *testing.T) {
	root := repoRoot(t)

	found := map[string]bool{}
	// readsTestCalls records, per subject file, whether it names the test-call
	// vocabulary at all. It is what turns each row's disposition from a claim
	// into an assertion — see the disposition-accuracy check below.
	readsTestCalls := map[string]bool{}
	{
		walkRoot := filepath.Join(root, "internal")
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
	// AND A NAMED FILE THE WALK MUST FIND. Non-emptiness alone is satisfied by
	// any one file, so a walk that resolved to the wrong tree — and this half is
	// now compiled in two layouts — would still pass it. This names the opts_in
	// renderer, which is also a row of the table above, so a walk that misses it
	// fails both directions at once.
	require.True(t, found["internal/tools/intercept_query_analyze_node.go"],
		"census control: the walk must find the opts_in renderer BY NAME, not merely find something")

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
