// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// TestCodeSearchOverfetchBackfillsHydrationDrops pins the result-set integrity
// fix: a search whose candidates are partly unhydratable must still render a full
// limit of results rather than silently shrinking.
//
// The recorded-k assertion is the CATCHER. Without it this test passes unchanged
// against an implementation that never over-fetches, because the shortfall it
// measures would then simply be a different number rather than a failure.
func TestCodeSearchOverfetchBackfillsHydrationDrops(t *testing.T) {
	const limit = 5

	// 20 ranked candidates; the node table resolves only every OTHER id, so half
	// of whatever is fetched is dropped by the hydrate.
	hits := make([]searchengine.Hit, 0, 20)
	nodes := make(map[string]*knowledgev1.Node, 10)
	for i := range 20 {
		id := fmt.Sprintf("f%d.go:S%d", i, i)
		hits = append(hits, searchengine.Hit{ID: id, Score: 1 - float64(i)/100})
		if i%2 == 0 {
			nodes[id] = &knowledgev1.Node{
				Id: id, SymbolName: fmt.Sprintf("S%d", i), Type: "function",
				FilePath: fmt.Sprintf("f%d.go", i), StartLine: 1,
			}
		}
	}

	f := &codeSearchEngineFake{
		hitsByRepo: map[string][]searchengine.Hit{"r": hits},
		nodes:      nodes,
	}

	results := searchOneCodeQuery(context.Background(), cdepsFor(f),
		&knowledgev1.GraphSelector{Graph: "code", Repo: "r"}, "q", nil, limit, "", "r")

	require.Equal(t, limit*codeSearchOverfetch, f.recordedK(),
		"the engine is asked for limit*codeSearchOverfetch candidates, not limit")
	require.Len(t, results, limit,
		"hydration drops are backfilled: a full limit of results survives (pre-fix this was 3)")
}

// degradeFakeDeps wires a codeSearchDeps the way composeCodeSearch does, degrade
// recorder included, so the render paths under test carry a live one.
func degradeFakeDeps(f *codeSearchEngineFake) codeSearchDeps {
	cd := cdepsFor(f)
	cd.degrade = &searchDegrade{}
	return cd
}

// healthyCodeSearchFake resolves one hit cleanly — the known-negative control
// every subtest below pairs its assertion with.
func healthyCodeSearchFake() *codeSearchEngineFake {
	return &codeSearchEngineFake{
		hitsByRepo: map[string][]searchengine.Hit{"r": {{ID: "a.go:A", Score: 0.9}}},
		nodes: map[string]*knowledgev1.Node{
			"a.go:A": {Id: "a.go:A", SymbolName: "A", Type: "function", FilePath: "a.go", StartLine: 1},
		},
	}
}

// erroringCodeSearchFake fails every Search — the degraded-leg probe.
func erroringCodeSearchFake() *codeSearchEngineFake {
	f := healthyCodeSearchFake()
	f.searchErr = errors.New("segment engine unavailable")
	return f
}

// TestCodeSearchDegradedMarker pins that a failed search leg reaches the caller
// instead of rendering as an ordinary empty result under a healthy banner.
//
// THREE separately-omissible render paths carry the marker, so each is its own
// subtest: one test asserting one body would leave two shippable-unwired. Each
// subtest pairs its assertion with the KNOWN-NEGATIVE control — the identical
// call on a healthy fake — so a marker printed unconditionally cannot pass.
func TestCodeSearchDegradedMarker(t *testing.T) {
	ctx := context.Background()
	args := codeSearchArgs{Graph: "code", Repo: "r", Text: "q"}

	t.Run("single_repo", func(t *testing.T) {
		res := composeCodeSearchSingleRepo(ctx, nil, degradeFakeDeps(erroringCodeSearchFake()),
			args, []string{"q"}, nil, 10, true, false)
		require.Contains(t, textBodyTools(res), searchDegradedMarker,
			"a failed search leg is surfaced, not swallowed")

		ok := composeCodeSearchSingleRepo(ctx, nil, degradeFakeDeps(healthyCodeSearchFake()),
			args, []string{"q"}, nil, 10, true, false)
		require.NotContains(t, textBodyTools(ok), searchDegradedMarker,
			"control: a healthy search carries no degrade marker")
	})

	t.Run("multi_repo", func(t *testing.T) {
		// The cross-repo fan-out detects each repo's branch from the machine-local
		// manifest, so an EMPTY temp manifest is what pins this subtest's meaning:
		// "r" reliably resolves to the no-manifest-entry state, which is silent by
		// design. Without it these assertions read the developer's real manifest,
		// and on a machine that happens to hold a repo named "r" the healthy
		// control below flips to a "branch detection failed for repo r" banner.
		withTestManifest(t)
		multiArgs := codeSearchArgs{Graph: "code", Repos: []string{"r"}, Text: "q"}
		res := composeCodeSearchMultiRepo(ctx, nil, degradeFakeDeps(erroringCodeSearchFake()),
			multiArgs, []string{"q"}, nil, 10, true, false)
		require.Contains(t, textBodyTools(res), searchDegradedMarker,
			"the multi-repo composer has its OWN builder and must be wired too")

		ok := composeCodeSearchMultiRepo(ctx, nil, degradeFakeDeps(healthyCodeSearchFake()),
			multiArgs, []string{"q"}, nil, 10, true, false)
		require.NotContains(t, textBodyTools(ok), searchDegradedMarker,
			"control: a healthy multi-repo search carries no degrade marker")
	})

	t.Run("json", func(t *testing.T) {
		jsonArgs := args
		jsonArgs.Format = "json"
		res := composeCodeSearchSingleRepo(ctx, nil, degradeFakeDeps(erroringCodeSearchFake()),
			jsonArgs, []string{"q"}, nil, 10, true, false)

		require.NotEmpty(t, res.Content)
		// BOTH halves matter. Asserting only the marker would pass an implementation
		// that prepended it and broke every json consumer.
		var envelope engine.SearchJSONResponse
		require.NoError(t, json.Unmarshal([]byte(res.Content[0].Text), &envelope),
			"content[0] is still the parseable json envelope")

		var marked bool
		for _, c := range res.Content[1:] {
			if c.Type == "text" && strings.Contains(c.Text, searchDegradedMarker) {
				marked = true
			}
		}
		require.True(t, marked, "a LATER content item carries the degrade marker")

		ok := composeCodeSearchSingleRepo(ctx, nil, degradeFakeDeps(healthyCodeSearchFake()),
			jsonArgs, []string{"q"}, nil, 10, true, false)
		require.Len(t, ok.Content, 1,
			"control: a healthy json search returns the envelope alone, with no extra item")
	})
}
