// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/engine"
)

// TestInterceptLogsQuery_ResolverRequiresLogsGraph pins the resolver disposition.
//
// resolver is the odd one out among the logs-family modes: pivot, correlations,
// explain and timeline all have a non-logs arm, but resolver exists ONLY inside
// the logs handler, which is reachable solely through this intercept — and that
// intercept declines every non-logs graph. So a non-logs resolver query was
// claimed by nobody and died at the generic engine deny.
//
// The disposition is a structured REFUSAL rather than a new capability: resolver
// reports per-stream cloud-resolution status for an ephemeral log graph and has
// no analog to compute on any other graph.
//
// The third row is the anti-false-rejection guard — green before and after — and
// exists because the obvious wrong fix is a guard that swallows every non-logs
// mode instead of just this one.
func TestInterceptLogsQuery_ResolverRequiresLogsGraph(t *testing.T) {
	for _, tc := range []struct {
		name        string
		args        map[string]any
		wantHandled bool
	}{
		{"resolver-no-graph", map[string]any{"mode": "resolver"}, true},
		{"resolver-knowledge-graph", map[string]any{"graph": "knowledge", "mode": "resolver"}, true},
		{"stats-still-falls-through", map[string]any{"graph": "knowledge", "mode": "stats"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deps := &interceptDeps{}
			handled, out := InterceptLogsQuery(opCtx(), deps, queryParams(t, tc.args))
			require.Equal(t, tc.wantHandled, handled, "%s claim disposition", tc.name)
			if !tc.wantHandled {
				return
			}
			require.True(t, out.IsError, "%s must be a structured refusal", tc.name)
			assert.Contains(t, engine.FirstTextContent(out), `requires graph="logs"`,
				"the refusal must carry the locked substring")
		})
	}
}
