// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"bytes"
	"encoding/json"
	"maps"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// Read-time provenance coverage for plan_tree's JSON payload. Split out
// of intercept_query_plan_tree_test.go rather than appended to it: that
// file sits near the repo's hard 500-line commit gate, and the parity
// suite there is organized around the captured goldens, which this
// timestamp contract is deliberately independent of.

// TestInterceptQueryPlanTree_JSONCarriesUpdatedAt is the catcher for
// buildPlanTreeJSON's read-time provenance key. It seeds a nonzero
// UpdatedAt on the plan root and on exactly ONE step, leaving every
// other node at zero, so the test pins both halves of the contract at
// once: the key is present with the raw unix-nanos value where the
// node carries a timestamp, and absent entirely where it does not.
// The zero-valued rows are what keep the key conditional — an
// unconditional `row["updated_at"]` would re-baseline plan_tree.json.golden.
func TestInterceptQueryPlanTree_JSONCarriesUpdatedAt(t *testing.T) {
	const seededTS = int64(1785548993004179000)
	const seededStep = "00000000000000000000000000000100"
	const bareStep = "00000000000000000000000000000101"

	f := newParityFixture()
	planID := seedPlanTreeFixture(f)
	f.nodes[planID].UpdatedAt = seededTS
	f.nodes[seededStep].UpdatedAt = seededTS

	deps := &parityDeps{gc: f.gc()}
	args := mustMarshal(t, map[string]any{"mode": "plan_tree", "id": planID, "format": "json"})

	handled, res := InterceptQueryPlanTree(opCtx(), deps, kgtools.CallToolParams{Name: "query", Arguments: args})
	require.True(t, handled)
	require.False(t, res.IsError, "intercept produced error: %v", res.Content)

	// UseNumber, not the default float64: a nanosecond timestamp
	// exceeds 2^53, so decoding it as a float would silently round and
	// the assertion would compare two equally-wrong values.
	dec := json.NewDecoder(bytes.NewReader([]byte(extractText(res))))
	dec.UseNumber()
	var root map[string]any
	require.NoError(t, dec.Decode(&root))

	assertUpdatedAtNanos(t, root, seededTS, "root row")

	rows := indexPlanTreeRowsByID(root)
	require.Contains(t, rows, seededStep, "seeded step must appear in the tree")
	require.Contains(t, rows, bareStep, "zero-timestamp step must appear in the tree")

	assertUpdatedAtNanos(t, rows[seededStep], seededTS, "the seeded step's row")

	assert.NotContains(t, rows[bareStep], "updated_at",
		"a step with a zero UpdatedAt must omit the key entirely")
}

// assertUpdatedAtNanos asserts row carries updated_at as the exact
// int64 nanosecond value, reading it through json.Number so the
// comparison never round-trips through a lossy float64.
func assertUpdatedAtNanos(t *testing.T, row map[string]any, want int64, label string) {
	t.Helper()
	raw, ok := row["updated_at"]
	require.True(t, ok, "%s must carry updated_at", label)
	num, ok := raw.(json.Number)
	require.True(t, ok, "%s updated_at must decode as a JSON number, got %T", label, raw)
	got, err := num.Int64()
	require.NoError(t, err, "%s updated_at must be an integer nanosecond value", label)
	assert.Equal(t, want, got, "%s updated_at", label)
}

// indexPlanTreeRowsByID flattens a buildPlanTreeJSON payload into an
// id → row map so per-node key assertions don't have to walk the
// nesting by hand.
func indexPlanTreeRowsByID(row map[string]any) map[string]map[string]any {
	out := map[string]map[string]any{}
	id, _ := row["id"].(string)
	if id != "" {
		out[id] = row
	}
	children, _ := row["children"].([]any)
	for _, c := range children {
		cr, ok := c.(map[string]any)
		if !ok {
			continue
		}
		maps.Copy(out, indexPlanTreeRowsByID(cr))
	}
	return out
}
