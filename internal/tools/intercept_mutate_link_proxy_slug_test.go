// SPDX-License-Identifier: Apache-2.0

// intercept_mutate_link_proxy_slug_test.go — where a practice proxy id's graph
// segment comes from, split out of intercept_mutate_link_test.go when that file
// crossed the repo's 500-line cap.
//
// IT IS ONE SUBJECT AND READS AS ONE FILE: the segment comes from the graph NAME
// the catalog reports rather than from anything the caller supplied, and the
// selector a caller might have supplied instead is refused. The two halves are
// the same fact from either side.

package tools

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// TestCrossGraphLink_ProxyIDNamesTheCatalogGraph covers the proxy-id clause: the
// id's graph segment is the NAME the foreign-graph enumeration reported, not a
// value the caller chose and not a transform of one.
//
// THERE IS NO SLUG PARITY LEFT TO ASSERT, and this test used to be it. It seeded
// a practice graph named "cplusplus" and pinned that the proxy id carried that
// spelling byte-for-byte, because the family held eight per-language graphs whose
// names were slugs of display strings; both the transform and those graphs are
// retired. What the test still proves is the part that made it pass either way:
// the id comes from the catalog, which reports only names a create channel
// admitted.
func TestCrossGraphLink_ProxyIDNamesTheCatalogGraph(t *testing.T) {
	// The ONE practice graph, under the only name it can carry. The probe
	// addresses it with no instance field, and the catalog name is what the
	// proxy id's graph segment is built from.
	const slug = "default"
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

	// THE LINK NAMES NO LANGUAGE, and it no longer can: every practice arm refuses
	// one. The proxy id's graph segment comes from the NAME the catalog reports,
	// which is what this test has always been about — the caller never selected
	// the graph, so the endpoint resolves by probing and the segment is whichever
	// graph answered.
	handled, res := InterceptMutate(opCtx(), deps, kgtools.CallToolParams{
		Name:      "mutate",
		Arguments: json.RawMessage(`{"operation":"link","graph":"practice","from":"dec-1","to":"pat-1","relationship":"uses"}`),
	})
	require.True(t, handled)
	require.False(t, res.IsError, "proxy link: %s", toolResultText(res))

	require.Len(t, fc.execMutations, 2)
	wantID := "proxy:practice:" + slug + ":pat-1"
	assert.Equal(t, "proxy:practice:default:pat-1", wantID,
		"the graph segment is the catalog's name for the one practice graph")
	assert.Equal(t, wantID, fc.execMutations[0].GetNodeBodies()[0].GetId())

	// THE REFUSAL LEG, so the paragraph above is a fact rather than a convention.
	// The same link carrying `language` is refused by name: the field addresses no
	// practice graph, and a write that quietly dropped it would materialize the
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
	require.True(t, refusedRes.IsError, "a practice write carrying `language` is refused")
	assert.Contains(t, toolResultText(refusedRes), "source_hub",
		"and the refusal names the replacement rather than merely declining")
	assert.Empty(t, refused.execMutations, "the refusal costs no write")
}
