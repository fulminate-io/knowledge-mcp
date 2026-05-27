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

func TestHandleSimulateClient_NoGraphClient_Errors(t *testing.T) {
	deps := interceptTestDeps{gc: &fakeGraphCaller{}}
	res := handleSimulateClient(context.Background(), deps, kgtools.CallToolParams{
		Name:      "query",
		Arguments: json.RawMessage(`{"mode":"simulate","action":"add_charge","target":"t-1","polarity":"positive","weight":2}`),
	})
	require.True(t, res.IsError)
	assert.Contains(t, toolResultText(res), "graph client unavailable")
}

func TestHandleSimulateClient_InvalidJSON_Errors(t *testing.T) {
	deps := interceptTestDeps{gc: &fakeGraphCaller{}}
	res := handleSimulateClient(context.Background(), deps, kgtools.CallToolParams{
		Name:      "query",
		Arguments: json.RawMessage(`not json`),
	})
	require.True(t, res.IsError)
	assert.Contains(t, toolResultText(res), "invalid arguments")
}
