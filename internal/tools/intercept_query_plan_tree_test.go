// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// Parity tests. Each intercept's parity check seeds an
// in-memory node+edge fixture, calls the intercept, and asserts byte
// equality against the pre-relocation golden captured in Phase 1.
// The scrub regexes match testdata_capture_test.go (build-tag
// goldengen, deleted in Phase 5).

// Scrubbers — must match scrubAll() in testdata_capture_test.go so
// the parity diff is meaningful. Duplicated here because the capture
// file is build-tagged out of the default test binary.
var (
	parityIDRegex            = regexp.MustCompile(`[0-9a-f]{32}`)
	parityUUIDRegex          = regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)
	parityRFC3339NanoRegex   = regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:\d{2})`)
	parityTimeRegex          = regexp.MustCompile(`\d{4}-\d{2}-\d{2} \d{2}:\d{2}`)
	parityShortIDInLineRegex = regexp.MustCompile(`id: [0-9a-f]{12}\)`)
)

// scrubForParity applies the same scrubbers the goldengen capture
// uses so re-runs produce stable bytes for the byte-equality assert.
func scrubForParity(s string) string {
	s = parityUUIDRegex.ReplaceAllString(s, "<UUID>")
	s = parityRFC3339NanoRegex.ReplaceAllString(s, "<TIMENANO>")
	s = parityIDRegex.ReplaceAllString(s, "<ID>")
	s = parityShortIDInLineRegex.ReplaceAllString(s, "id: <SHORTID>)")
	s = parityTimeRegex.ReplaceAllString(s, "<TIME>")
	return s
}

// readGolden loads testdata/<name>.golden.
func readGolden(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	b, err := os.ReadFile(path)
	require.NoError(t, err, "read golden %s", path)
	return string(b)
}

// mustMarshal panics on json.Marshal failure. Test-only helper that
// suppresses the errchkjson lint on test payloads we know are pure
// string-keyed maps. Failure here would be a test bug, not a
// production concern.
func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v) //nolint:errchkjson // test payloads; failure = test bug
	require.NoError(t, err)
	return b
}

func TestInterceptQueryPlanTree_TextFormat_ByteIdentical_ToGolden(t *testing.T) {
	f := newParityFixture()
	planID := seedPlanTreeFixture(f)
	deps := &parityDeps{gc: f.gc()}

	args, err := json.Marshal(map[string]any{"mode": "plan_tree", "id": planID})
	require.NoError(t, err)

	handled, res := InterceptQueryPlanTree(opCtx(), deps, kgtools.CallToolParams{Name: "query", Arguments: args})
	require.True(t, handled)
	require.False(t, res.IsError, "intercept produced error: %v", res.Content)

	got := scrubForParity(extractText(res))
	want := readGolden(t, "plan_tree")
	assert.Equal(t, want, got, "plan_tree text output diverged from golden")
}

func TestInterceptQueryPlanTree_JSONFormat_ByteIdentical_ToGolden(t *testing.T) {
	f := newParityFixture()
	planID := seedPlanTreeFixture(f)
	deps := &parityDeps{gc: f.gc()}

	args, err := json.Marshal(map[string]any{"mode": "plan_tree", "id": planID, "format": "json"})
	require.NoError(t, err)

	handled, res := InterceptQueryPlanTree(opCtx(), deps, kgtools.CallToolParams{Name: "query", Arguments: args})
	require.True(t, handled)
	require.False(t, res.IsError, "intercept produced error: %v", res.Content)

	got := scrubForParity(extractText(res))
	want := readGolden(t, "plan_tree.json")
	assert.Equal(t, want, got, "plan_tree.json output diverged from golden")
}

// TestInterceptQueryPlanTree_TombstonedChild_DroppedFromBothPaths is the
// fails-when-absent test for tombstone parity: a tombstoned child must render in
// NEITHER the text nor the json output. The parityCaller traversal arm drops
// edges whose peer is tombstoned (mirroring the server's unconditional
// edgeFilteredByTombstone), so a regression in BuildChildIndex or the traversal
// that let a tombstoned peer through would surface the child here and fail.
func TestInterceptQueryPlanTree_TombstonedChild_DroppedFromBothPaths(t *testing.T) {
	f := newParityFixture()
	planID := seedPlanTreeFixture(f)

	// Add one extra step under phase-1 and tombstone it.
	const phase1 = "00000000000000000000000000000010"
	const tombStep = "00000000000000000000000000000099"
	f.add(&knowledgev1.Node{Id: tombStep, Type: string(kgtypes.NodeStep), SymbolName: "tombstoned-step",
		Status: "pending", Description: "should never render"})
	f.link(phase1, tombStep)
	f.tombstone(tombStep)

	deps := &parityDeps{gc: f.gc()}

	textArgs, err := json.Marshal(map[string]any{"mode": "plan_tree", "id": planID})
	require.NoError(t, err)
	_, textRes := InterceptQueryPlanTree(opCtx(), deps, kgtools.CallToolParams{Name: "query", Arguments: textArgs})
	require.False(t, textRes.IsError)
	require.NotContains(t, extractText(textRes), "tombstoned-step", "tombstoned child must not render in text")

	jsonArgs, err := json.Marshal(map[string]any{"mode": "plan_tree", "id": planID, "format": "json"})
	require.NoError(t, err)
	_, jsonRes := InterceptQueryPlanTree(opCtx(), deps, kgtools.CallToolParams{Name: "query", Arguments: jsonArgs})
	require.False(t, jsonRes.IsError)
	require.NotContains(t, extractText(jsonRes), "tombstoned-step", "tombstoned child must not render in json")
}

// TestBuildPlanTreeJSON_NoChildIndexEntry_OmitsChildrenKey pins the accepted
// post-fix contract: a node with no childIndex entry (its only structure edge
// pointed at a tombstoned/absent node, so nothing was indexed under it) yields a
// JSON row with NO "children" key — not an empty "children":[] array.
func TestBuildPlanTreeJSON_NoChildIndexEntry_OmitsChildrenKey(t *testing.T) {
	node := &knowledgev1.Node{Id: "leaf", Type: string(kgtypes.NodeStep), SymbolName: "leaf", Status: "pending"}
	// Empty index → no entry for "leaf".
	// No projection: the fixed key set, which is the shape this contract is about.
	row := buildPlanTreeJSON(node, 0, 10, map[string][]*knowledgev1.Node{}, nil, nil)

	_, hasChildren := row["children"]
	assert.False(t, hasChildren, "a node with no indexed children must omit the children key entirely")
	assert.Equal(t, "leaf", row["id"])
}

func TestInterceptQueryPlanTree_WrongTool_FallsThrough(t *testing.T) {
	deps := &parityDeps{gc: newParityFixture().gc()}
	args := mustMarshal(t, map[string]any{"mode": "plan_tree", "id": "anything"})
	handled, _ := InterceptQueryPlanTree(opCtx(), deps, kgtools.CallToolParams{Name: "search", Arguments: args})
	assert.False(t, handled, "wrong tool must fall through")
}

func TestInterceptQueryPlanTree_WrongMode_FallsThrough(t *testing.T) {
	deps := &parityDeps{gc: newParityFixture().gc()}
	args := mustMarshal(t, map[string]any{"mode": "examine", "id": "anything"})
	handled, _ := InterceptQueryPlanTree(opCtx(), deps, kgtools.CallToolParams{Name: "query", Arguments: args})
	assert.False(t, handled, "wrong mode must fall through")
}

func TestInterceptQueryPlanTree_MissingID_Errors(t *testing.T) {
	deps := &parityDeps{gc: newParityFixture().gc()}
	args := mustMarshal(t, map[string]any{"mode": "plan_tree"})
	handled, res := InterceptQueryPlanTree(opCtx(), deps, kgtools.CallToolParams{Name: "query", Arguments: args})
	require.True(t, handled)
	require.True(t, res.IsError)
	assert.Contains(t, extractText(res), "plan_tree mode requires 'id' parameter")
}

// (extractText lives in intercept_add_criterion.go and is reused
// by every parity test in this package.)
//
// The plan_tree read-time-provenance test lives in
// intercept_query_plan_tree_updated_at_test.go — split out to keep this
// file under the repo's hard 500-line commit gate.
