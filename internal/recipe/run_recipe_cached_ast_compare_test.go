// SPDX-License-Identifier: Apache-2.0

package recipe

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// compareRecipeBody is a recipe whose predicate is a COMPARE leaf, so the leaf
// under test is one the astCache holds and two concurrent runs both reach.
const compareRecipeBody = `select section
filter {"compare": {"of": "section.metadata.rank", "op": "gt", "value": "1"}}
emit pattern {
    name := section.symbol_name
}`

// compareRecipeCaller mirrors fullRecipeCaller but stamps a NUMERIC `rank` key
// the compare leaf can read and the metadata-key census can admit. Two of the
// three sections clear the threshold, so a run that silently dropped every row
// is distinguishable from one that answered correctly.
func compareRecipeCaller() *routingCaller {
	return &routingCaller{
		nodesByGraph: map[string][]*knowledgev1.Node{
			string(kgtypes.GraphWebRaw): {
				{Id: "s1", Type: "section", SymbolName: "Message Router", Metadata: map[string]string{"rank": "2"}},
				{Id: "s2", Type: "section", SymbolName: "Message Channel", Metadata: map[string]string{"rank": "3"}},
				{Id: "s3", Type: "section", SymbolName: "Preface", Metadata: map[string]string{"rank": "1"}},
			},
		},
		edgesByGraph: map[string][]*knowledgev1.Edge{
			string(kgtypes.GraphWebRaw): {{FromId: "s1", ToId: "s2", Type: "relates-to"}},
		},
	}
}

// TestRunRecipe_ConcurrentRunsShareTheCachedAST_CompareLeaf pins the compare
// leaf to the same read-only-cached-AST property its matches sibling is pinned
// to: the resolved operator and parsed operand live in a PER-RUN map on Env,
// keyed by leaf pointer, and nothing writes to the shared tree.
//
// IT IS A SECOND CONCURRENCY TEST RATHER THAN A WIDENING OF THE LANDED ONE, and
// that is the whole point: the landed body carries a MATCHES leaf, so it stays
// green against a compare leaf that writes its resolution onto the shared tree.
// Only a body whose predicate is a compare leaf puts the new write on the racing
// path.
//
// RUN IT WITH -race. Without the detector this passes against a racing build,
// because both writers store the same resolution; the race is the finding, not a
// wrong answer.
func TestRunRecipe_ConcurrentRunsShareTheCachedAST_CompareLeaf(t *testing.T) {
	// Three independent callers running the SAME body, so the astCache key is
	// identical and the parsed tree is shared while no fake is.
	// parseInlineWithCache keys on a hash of the body's CONTENT, which is what
	// keeps the racing path intact now that the runs are inline.
	callers := make([]*routingCaller, 3)
	for i := range callers {
		callers[i] = compareRecipeCaller()
	}
	opts := inlineOpts(compareRecipeBody)

	// Warm the cache: after this the *Recipe both goroutines below reach — and
	// therefore the *CompareLeaf they key their per-run maps on — is the cached
	// one rather than one each parsed for itself.
	_, err := RunRecipe(context.Background(), callers[0], "src-graph", kgtypes.GraphWebRaw, opts)
	require.NoError(t, err)

	var wg sync.WaitGroup
	errs := make([]error, 2)
	results := make([]*Result, 2)
	for i := range 2 {
		wg.Go(func() {
			results[i], errs[i] = RunRecipe(context.Background(), callers[i+1], "src-graph", kgtypes.GraphWebRaw, opts)
		})
	}
	wg.Wait()

	for i, err := range errs {
		require.NoError(t, err, "concurrent run %d must not be refused", i)
		require.NotNil(t, results[i])
		require.NotNil(t, results[i].Extract)
		require.Len(t, results[i].Extract.Rows, 2,
			"concurrent run %d must emit both sections above the threshold", i)
	}
}
