// SPDX-License-Identifier: Apache-2.0

// intercept_mutate_link_proxy_slug_test.go — the practice PROXY-ID slug parity,
// split out of intercept_mutate_link_test.go when that file crossed the repo's
// 500-line cap.
//
// IT IS ONE SUBJECT AND READS AS ONE FILE: the proxy id's slug comes from the
// graph NAME the catalog reports, and the legacy selector that used to choose
// that graph is refused on a write. The two halves are the same fact from
// either side.

package tools

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// TestCrossGraphLink_ProxySlugParity covers the proxy-id clause: a practice
// graph whose name is non-trivial ("cplusplus") produces the byte-identical
// proxy id the server addresses it by.
//
// THERE IS NO LONGER A SECOND SIDE TO THIS PARITY, and the comment used to name
// one. It said the id matched "what the server's slugifyLanguage would" produce;
// that wrapper is gone, and no resolution path transforms a graph name any more.
// What actually makes this test pass, unchanged, is that the client proxy path
// uses the graph NAME the catalog reports — which is already canonical, because
// every create channel REFUSES a name that is not.
func TestCrossGraphLink_ProxySlugParity(t *testing.T) {
	// The practice graph for "C++" is named by its slug ("cplusplus") — that is
	// what the foreign-graph enumeration reports and what the node probe
	// resolves against. The slug
	// is the deterministic SlugifyLanguage("C++") output, inlined here: the client
	// proxy path uses the graph NAME the fake reports (already a slug), so this
	// test seeds + asserts that slug literally.
	const slug = "cplusplus"
	fc := &fakeGraphCaller{
		queryResponsesByGraph: map[string]map[string]kgtools.ToolResult{
			"knowledge": {"dec-1": graphNodeResult(t, "dec-1", "decision", "Dec", "d")},
		},
		queryResponsesByGraphName: map[graphKey]map[string]kgtools.ToolResult{
			{Type: "practice", Name: slug}: {"pat-1": graphNodeResult(t, "pat-1", "pattern", "Pat", "p")},
		},
		listGraphsResult: listGraphsResultFor(t, [2]string{"practice", slug}),
	}
	deps := interceptTestDeps{gc: fc}

	// THE LINK NAMES NO LANGUAGE, and it no longer can: a practice WRITE refuses
	// one. The proxy id's slug still comes from the graph NAME the catalog
	// reports, which is what this test has always been about — what changed is
	// that the caller no longer selects the graph, so the endpoint resolves by
	// probing and the slug is whichever graph answered.
	handled, res := InterceptMutate(opCtx(), deps, kgtools.CallToolParams{
		Name:      "mutate",
		Arguments: json.RawMessage(`{"operation":"link","graph":"practice","from":"dec-1","to":"pat-1","relationship":"uses"}`),
	})
	require.True(t, handled)
	require.False(t, res.IsError, "proxy link: %s", toolResultText(res))

	require.Len(t, fc.execMutations, 2)
	wantID := "proxy:practice:" + slug + ":pat-1"
	assert.Equal(t, "proxy:practice:cplusplus:pat-1", wantID, "slug parity: C++ → cplusplus")
	assert.Equal(t, wantID, fc.execMutations[0].GetNodeBodies()[0].GetId())

	// THE REFUSAL LEG, so the paragraph above is a fact rather than a convention.
	// The same link carrying the legacy selector is refused by name: `language` is
	// a READ selector, and a write that quietly dropped it would materialize the
	// proxy against whichever graph happened to answer the probe.
	refused := &fakeGraphCaller{
		queryResponsesByGraph: map[string]map[string]kgtools.ToolResult{
			"knowledge": {"dec-1": graphNodeResult(t, "dec-1", "decision", "Dec", "d")},
		},
		queryResponsesByGraphName: map[graphKey]map[string]kgtools.ToolResult{
			{Type: "practice", Name: slug}: {"pat-1": graphNodeResult(t, "pat-1", "pattern", "Pat", "p")},
		},
		listGraphsResult: listGraphsResultFor(t, [2]string{"practice", slug}),
	}
	_, refusedRes := InterceptMutate(opCtx(), interceptTestDeps{gc: refused}, kgtools.CallToolParams{
		Name:      "mutate",
		Arguments: json.RawMessage(`{"operation":"link","graph":"practice","language":"C++","from":"dec-1","to":"pat-1","relationship":"uses"}`),
	})
	require.True(t, refusedRes.IsError, "a practice write carrying the legacy selector is refused")
	assert.Contains(t, toolResultText(refusedRes), "source_hub",
		"and the refusal names the replacement rather than merely declining")
	assert.Empty(t, refused.execMutations, "the refusal costs no write")
}
