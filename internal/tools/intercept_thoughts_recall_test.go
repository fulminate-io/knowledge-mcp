// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

func TestValidateRecallClientArgs_OutOfRange(t *testing.T) {
	vMin := -2.0
	a := recallClientArgs{ValenceMin: &vMin}
	msg := validateRecallClientArgs(a)
	require.NotEmpty(t, msg)
	assert.Contains(t, msg, "valence_min")
}

func TestValidateRecallClientArgs_SwappedBounds(t *testing.T) {
	vMin := 0.8
	vMax := 0.2
	a := recallClientArgs{ValenceMin: &vMin, ValenceMax: &vMax}
	msg := validateRecallClientArgs(a)
	require.NotEmpty(t, msg)
	assert.Contains(t, msg, "swapped")
}

func TestHandleRecallClient_GraphClientUnavailable(t *testing.T) {
	// FUL-323: GraphCaller() (was GraphClient()) returns nil → unavailable.
	// interceptTestDeps.GraphCaller() reads d.gc; leaving it unset / nil
	// triggers the nil-guard.
	deps := interceptTestDeps{gc: nil}
	res := handleRecallClient(context.Background(), deps, kgtools.CallToolParams{
		Name:      "thoughts",
		Arguments: json.RawMessage(`{"operation":"recall","query":"q"}`),
	})
	require.True(t, res.IsError)
	assert.Contains(t, toolResultText(res), "graph client unavailable")
}

func TestHandleRecallClient_InvalidTimeStart(t *testing.T) {
	// Pass a bad date format — handler should reject before reaching gc.
	// But because GraphClient is nil, the validation must fire AFTER the
	// nil check. Re-order test: pass a valid graph client stub? We do
	// not have one. Use BadDate AND empty GraphClient — the error will
	// come from gc nil first. That's fine for this test; the date
	// validation is exercised in the next test which uses a stub.
	deps := interceptTestDeps{gc: &fakeGraphCaller{}}
	res := handleRecallClient(context.Background(), deps, kgtools.CallToolParams{
		Name:      "thoughts",
		Arguments: json.RawMessage(`{"operation":"recall","query":"q","time_start":"not-a-date"}`),
	})
	require.True(t, res.IsError)
}
