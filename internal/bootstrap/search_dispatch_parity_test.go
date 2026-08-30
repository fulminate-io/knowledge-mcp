// SPDX-License-Identifier: Apache-2.0

package bootstrap

// search_dispatch_parity_test.go holds the SEARCH-tool half of the dispatch
// parity suite: its own chain driver and the two arm-routing tests that use it.
//
// SPLIT FROM query_dispatch_parity_test.go FOR THE LINE BUDGET, and along the
// seam that costs a reader least — `search` is a separate compile path from
// `query`, driven through the chain under its own tool name, so these tests never
// touched the query driver, the shape grid or the partition guard they used to
// sit beside. The lefthook file-length gate globs *.go with only vendor/** and
// gen/** excluded, so a test file hits the identical 500-line commit block a
// source file does.

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// driveSearchParity is the search-shaped sibling of driveQueryParity. The
// `search` tool is a separate compile path from `query`, so its cells are
// driven through the chain under their own tool name rather than by bending the
// query driver. There is no engine fall-through here: the knowledge/default
// search arm claims unconditionally, so an unclaimed search would show up as an
// empty body rather than a deny.
func driveSearchParity(t *testing.T, c *client, args map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(args)
	require.NoError(t, err)

	_, _, res := c.runInterceptChain(opCtx(), kgtools.CallToolParams{Name: "search", Arguments: raw})
	if len(res.Content) == 0 {
		return ""
	}
	return res.Content[0].Text
}

// TestSearchDispatchParity_GraphlessRepoSearchIsCodeArm pins which arm actually
// answers a search whose graph was omitted but whose repo was named. The repo
// gate has already decided that shape targets code; before the fix the search
// arm behind it re-read the raw graph field, saw empty, and served it from the
// knowledge corpus instead.
//
// The second sub-case is a STANDING CONTROL, not decoration.
// knowledgeSearchArmMarker is proven live elsewhere in this file only on
// QUERY-shaped cells, and these two cells reach the arm through a brand-new
// search-shaped driver. Without a case that must SEE the marker, an absent
// marker in the first case could equally mean "the code arm served it" (the
// pass we want) or "this driver never produces the marker at all" (a dead probe
// reading green). Asserting both directions removes that ambiguity on every
// future run rather than once at review time.
func TestSearchDispatchParity_GraphlessRepoSearchIsCodeArm(t *testing.T) {
	t.Run("graphless_repo_is_code_arm", func(t *testing.T) {
		c, _ := newParityClient(t)
		body := driveSearchParity(t, c, map[string]any{"query": "x", "repo": "knowledge"})
		assert.NotContainsf(t, body, knowledgeSearchArmMarker,
			"a graphless search naming a repo must land on the CODE arm — the repo gate already "+
				"decided that. Observed: %s", body)
	})

	t.Run("explicit_knowledge_is_knowledge_arm", func(t *testing.T) {
		c, _ := newParityClient(t)
		body := driveSearchParity(t, c, map[string]any{
			"query": "x", "graph": "knowledge", "repo": "knowledge",
		})
		assert.Containsf(t, body, knowledgeSearchArmMarker,
			"an explicit knowledge search must land on the knowledge arm — without this the "+
				"sibling case could pass on a probe that never produces the marker. Observed: %s", body)
	})
}

// TestSearchDispatchParity_RegisteredGraphTwin drives the registered
// custom-graph read cells through the REAL chain. The per-arm tests in package
// tools prove the claim predicate and the shared tail; only here can it be
// observed that no EARLIER member of runQueryDomainIntercepts takes these shapes
// first, and that the refusal cell is refused pre-Compile rather than after a
// read ran.
//
// The knowledge control is a STANDING CONTROL, not decoration — the same
// construction TestSearchDispatchParity_GraphlessRepoSearchIsCodeArm documents.
// Without a cell that MUST see one marker while the other is absent, an
// assertion about marker absence could pass on a driver that never produces
// either. It also fences the relaxed claim gate: the custom arm must not start
// swallowing knowledge queries.
func TestSearchDispatchParity_RegisteredGraphTwin(t *testing.T) {
	t.Run("custom_default_mode_type_text_is_claimed", func(t *testing.T) {
		c, eng := newParityClient(t)
		got := driveQueryParity(t, c, eng, map[string]any{
			"graph": "hellograph", "name": "demo", "type": "fact", "text": "auth",
		})

		assert.Truef(t, got.handled,
			"a default-mode custom-graph text search carrying a type filter must be CLAIMED — "+
				"declining it drops the shape onto an engine path that hard-errors for a custom "+
				"graph. Observed: %s", got.body)
		assert.Containsf(t, got.body, registeredGraphSearchArmMarker,
			"the claim must land on the registered custom-graph search arm. Observed: %s", got.body)
		assert.Equalf(t, int32(0), got.execDelta,
			"the arm answers before any Execute RPC. Observed: %s", got.body)
	})

	t.Run("custom_explicit_mode_type_text_is_claimed", func(t *testing.T) {
		c, eng := newParityClient(t)
		got := driveQueryParity(t, c, eng, map[string]any{
			"graph": "hellograph", "name": "demo", "type": "fact", "text": "auth", "mode": "text",
		})

		assert.Truef(t, got.handled,
			"an explicit-mode custom-graph text search carrying a type filter must be CLAIMED. "+
				"Observed: %s", got.body)
		assert.Containsf(t, got.body, registeredGraphSearchArmMarker,
			"the claim must land on the registered custom-graph search arm. Observed: %s", got.body)
		assert.Equalf(t, int32(0), got.execDelta,
			"the arm answers before any Execute RPC. Observed: %s", got.body)
	})

	t.Run("custom_id_plus_type_is_refused_by_precheck", func(t *testing.T) {
		c, eng := newParityClient(t)
		got := driveQueryParity(t, c, eng, map[string]any{
			"graph": "hellograph", "name": "demo", "id": "h1", "type": "fact",
		})

		assert.Falsef(t, got.handled,
			"an id-plus-filter payload is a by-id read, not a search — the custom arm must decline "+
				"it. Observed: %s", got.body)
		assert.Containsf(t, got.body, refusedByIDSelectorMarker,
			"the deliberately non-graph-gated by-id refusal covers custom graphs too, which is what "+
				"lets the relaxed claim gate keep declining id shapes without leaving them silently "+
				"unfiltered. Observed: %s", got.body)
		assert.Equalf(t, int32(0), got.execDelta,
			"the refusal happened pre-Compile, not after a read ran. Observed: %s", got.body)
	})

	t.Run("knowledge_control_still_lands_on_the_knowledge_arm", func(t *testing.T) {
		c, eng := newParityClient(t)
		got := driveQueryParity(t, c, eng, map[string]any{
			"graph": "knowledge", "text": "auth", "type": "finding",
		})

		assert.Containsf(t, got.body, knowledgeSearchArmMarker,
			"the equivalent knowledge payload must still land on the knowledge arm. Observed: %s",
			got.body)
		assert.NotContainsf(t, got.body, registeredGraphSearchArmMarker,
			"the relaxed custom-graph gate must not start swallowing knowledge queries. Observed: %s",
			got.body)
	})
}
