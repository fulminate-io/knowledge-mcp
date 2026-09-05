// SPDX-License-Identifier: Apache-2.0

package recipe

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// TestRunRecipe_ConcurrentRunsShareTheCachedAST pins the ONE property that
// makes astCache safe to share: a parsed *Recipe is READ-ONLY once it is in the
// cache.
//
// WHY IT NEEDS A TEST AT ALL. astCache is a process-global sync.Map keyed by a
// hash of the recipe BODY, and the daemon serves /mcp with no serializing lock —
// concurrent tool calls across sessions are a designed property, so two sessions
// running the same body reach the same *Recipe. Nothing about that is
// visible from any single-run test: every correctness assertion in this package
// passes whether the run mutates the shared tree or not, and the only difference
// is a race the runtime detector sees and a reader does not.
//
// THE OBSERVABLE FAILURE IS A CORRECT RECIPE ERRORING OUT, not merely a memory-
// model verdict: a goroutine reading a matches leaf's compiled regex while
// another writes it can see nil, and the evaluator then refuses the run as a
// validator bug that never happened.
//
// RUN IT WITH -race. Without the detector this test passes against a racing
// build, because both writers store the same value; the race is the finding, not
// a wrong answer. It is scoped to this one test by name for exactly that reason.
func TestRunRecipe_ConcurrentRunsShareTheCachedAST(t *testing.T) {
	const body = `select section
filter {"matches": {"of": "section.symbol_name", "regex": "^Message"}}
emit pattern {
    name := section.symbol_name
}`
	// Three independent callers, each running the SAME body, so the astCache key
	// is identical and the parsed tree is shared while no fake is.
	// parseInlineWithCache keys on a hash of the body's CONTENT, which is what
	// keeps the racing path intact now that the runs are inline.
	callers := make([]*routingCaller, 3)
	for i := range callers {
		callers[i] = fullRecipeCaller()
	}
	opts := inlineOpts(body)

	// Warm the cache: after this the *Recipe both goroutines below reach is the
	// cached one rather than one each parsed for itself.
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
			"concurrent run %d must emit both matching sections", i)
	}
}
