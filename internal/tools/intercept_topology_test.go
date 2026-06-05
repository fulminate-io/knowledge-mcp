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

// TestInterceptTopology_NonTopologyQuery_FallsThrough verifies the
// intercept passes through query calls that aren't dead_code.
func TestInterceptTopology_NonTopologyQuery_FallsThrough(t *testing.T) {
	deps := &repoTestDeps{rootDir: t.TempDir()}
	handled, _ := InterceptTopology(context.Background(), deps,
		kgtools.CallToolParams{Name: "query", Arguments: json.RawMessage(`{"text":"x"}`)})
	assert.False(t, handled)
}

// TestInterceptTopology_DSM_ClaimedLocally verifies the behavior: every
// non-dead_code analyzer (dsm, centrality, etc.) is CLAIMED client-side and runs
// LOCALLY via the foundation registry (each analyzer fetches its own nodes/edges
// over the wire). Here the test deps have a nil GraphCaller, so runLocalTopology
// returns a typed "graph caller unavailable" error before any wire read — but
// handled==true proves the claim, and the error message proves the local path
// (NOT a server Topology RPC, which no longer exists).
func TestInterceptTopology_DSM_ClaimedLocally(t *testing.T) {
	deps := &repoTestDeps{rootDir: t.TempDir()}
	handled, res := InterceptTopology(context.Background(), deps,
		kgtools.CallToolParams{Name: "query", Arguments: json.RawMessage(`{"mode":"topology","algorithm":"dsm","graph":"code","repo":"knowledge"}`)})
	assert.True(t, handled, "non-dead_code topology is now claimed client-side and run locally")
	require.True(t, res.IsError, "nil GraphCaller surfaces a typed error on the local path")
	assert.Contains(t, res.Content[0].Text, "graph caller unavailable")
}

// TestInterceptTopology_UnknownAnalyzer_Errors verifies an unregistered analyzer
// name is rejected with a typed error (the foundation.Get miss), proving the
// dispatch consults the registry rather than blindly forwarding.
func TestInterceptTopology_UnknownAnalyzer_Errors(t *testing.T) {
	deps := &repoTestDeps{rootDir: t.TempDir(), gc: &fakeGraphCaller{}}
	handled, res := InterceptTopology(context.Background(), deps,
		kgtools.CallToolParams{Name: "query", Arguments: json.RawMessage(`{"mode":"topology","algorithm":"no_such_analyzer","graph":"knowledge"}`)})
	assert.True(t, handled)
	require.True(t, res.IsError)
	assert.Contains(t, res.Content[0].Text, "unknown analyzer")
}

// TestInterceptTopology_DeadCode_NoRepo_Errors verifies that without
// explicit repo: and without a resolver match, the intercept errors
// out before any wire activity.
func TestInterceptTopology_DeadCode_NoRepo_Errors(t *testing.T) {
	deps := &repoTestDeps{
		rootDir:  t.TempDir(),
		resolver: buildResolver(t, "other"),
	}
	handled, res := InterceptTopology(context.Background(), deps,
		kgtools.CallToolParams{Name: "query", Arguments: json.RawMessage(`{"mode":"topology","algorithm":"dead_code","graph":"code"}`)})
	assert.True(t, handled)
	require.True(t, res.IsError)
	assert.Contains(t, res.Content[0].Text, "repo is required")
}

// TestInterceptTopology_DeadCode_NonGoRepo_ReturnsEmptyFindings verifies
// the intercept handles non-Go cwds gracefully — runRTA returns a
// diagnostic, the intercept renders nil findings as the empty JSON array.
func TestInterceptTopology_DeadCode_NonGoRepo_ReturnsEmptyFindings(t *testing.T) {
	// Empty temp dir: no go.mod, no .go files. runRTA returns a
	// diagnostic ("no packages found" or "packages.Load failed") and
	// RunDeadCode returns (nil, nil).
	dir := t.TempDir()
	deps := &repoTestDeps{
		rootDir:  dir,
		resolver: buildResolver(t, "knowledge"),
		// GraphCaller must be non-nil so the intercept gets past the
		// "graph caller unavailable" guard. runRTA aborts before any
		// gc.Call is issued in this test, so the fake's Call body never
		// fires — fakeGraphCaller's default branch returns mutateResult.
		gc: &fakeGraphCaller{},
	}
	// Provide an explicit repo so resolveTopologyRepo succeeds.
	handled, res := InterceptTopology(context.Background(), deps,
		kgtools.CallToolParams{Name: "query", Arguments: json.RawMessage(`{"mode":"topology","algorithm":"dead_code","graph":"code","repo":"knowledge"}`)})
	assert.True(t, handled)
	assert.False(t, res.IsError)
	// nil findings render as "null" via json.MarshalIndent on a nil slice.
	// Either "null" or "[]" is acceptable — assert it's one of the two
	// empty shapes (the renderer chooses).
	body := res.Content[0].Text
	assert.Contains(t, []string{"null", "[]"}, body)
}

// TestInterceptTopology_RequiresGraphAndAlgorithm verifies query(mode:"topology")
// demands an explicit graph + algorithm: a missing graph or a missing algorithm
// is a CLAIMED (handled==true) validation error, not a fallthrough. The
// algorithm error lists the available analyzers (including the special-cased
// dead_code, which does not self-register in foundation).
func TestInterceptTopology_RequiresGraphAndAlgorithm(t *testing.T) {
	deps := &repoTestDeps{rootDir: t.TempDir(), gc: &fakeGraphCaller{}}

	// Missing graph → claimed error naming the requirement.
	handled, res := InterceptTopology(context.Background(), deps,
		kgtools.CallToolParams{Name: "query", Arguments: json.RawMessage(`{"mode":"topology","algorithm":"pagerank"}`)})
	require.True(t, handled)
	require.True(t, res.IsError)
	assert.Contains(t, res.Content[0].Text, `requires "graph"`)

	// Missing algorithm → claimed error listing the registered analyzers.
	handled, res = InterceptTopology(context.Background(), deps,
		kgtools.CallToolParams{Name: "query", Arguments: json.RawMessage(`{"mode":"topology","graph":"code"}`)})
	require.True(t, handled)
	require.True(t, res.IsError)
	assert.Contains(t, res.Content[0].Text, `requires "algorithm"`)
	assert.Contains(t, res.Content[0].Text, "Available analyzers:")
	assert.Contains(t, res.Content[0].Text, "dead_code")
}
