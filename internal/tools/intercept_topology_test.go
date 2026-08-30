// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	clienttopo "github.com/fulminate-io/knowledge-mcp/internal/topology"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/corpusscan"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

// The path_prefix honoring seam, test side. The PRODUCTION map names only
// corpus_scan, and it must never name a test symbol — but the arm-parity
// harness drives armTopology with the registered probe analyzer, and once
// path_prefix is classified CONSUMED the harness injects a value for it. Under
// a production-only map that injection would be refused, res.IsError would go
// true, and queryParityAssertBehaved would fail a CORRECT implementation. Adding
// the probe here is what keeps the harness drivable, and it is also what gives
// TestInterceptTopology_PathPrefixRouted a honoring analyzer to drive: the
// refusal runs after foundation.Get, so the positive case needs a registered
// analyzer whose name is in the map.
func init() { pathPrefixHonoringAnalyzers[qpTopologyAnalyzer] = true }

// TestInterceptTopology_PathPrefixRouted asserts BOTH directions of the
// honoring allowlist: path_prefix reaches Request.PathPrefix for an analyzer on
// the list, and is REFUSED by name for one that is not. The refusal half is the
// discriminating leg — routing the param to all 33 analyzers would satisfy the
// positive half alone while handing 32 of them a control they ignore.
func TestInterceptTopology_PathPrefixRouted(t *testing.T) {
	t.Run("honoring analyzer receives it", func(t *testing.T) {
		qpLastTopologyRequest = foundation.Request{}
		deps := &repoTestDeps{rootDir: t.TempDir(), gc: &fakeGraphCaller{}}
		handled, res := InterceptTopology(context.Background(), deps,
			kgtools.CallToolParams{Name: "query", Arguments: json.RawMessage(
				`{"mode":"topology","algorithm":"` + qpTopologyAnalyzer + `","graph":"knowledge","path_prefix":"cmd/knowledge/internal"}`)})
		require.True(t, handled)
		require.False(t, res.IsError, "a honoring analyzer must be served, not refused: %s", res.Content[0].Text)
		assert.Equal(t, "cmd/knowledge/internal", qpLastTopologyRequest.PathPrefix,
			"path_prefix must reach Request.PathPrefix rather than being dropped")
		// The known-positive control for the field read: RepoRoot is an
		// EXISTING routed field on the same Request literal, so its arrival
		// proves the assertion above is reading a live Request rather than a
		// zero value left over from a dispatch that never happened.
		assert.NotEmpty(t, qpLastTopologyRequest.RepoRoot)
	})

	t.Run("non-honoring analyzer is refused by name", func(t *testing.T) {
		deps := &repoTestDeps{rootDir: t.TempDir(), gc: &fakeGraphCaller{}}
		handled, res := InterceptTopology(context.Background(), deps,
			kgtools.CallToolParams{Name: "query", Arguments: json.RawMessage(
				`{"mode":"topology","algorithm":"dsm","graph":"code","repo":"knowledge","path_prefix":"cmd"}`)})
		require.True(t, handled)
		require.True(t, res.IsError, "path_prefix must be refused for an analyzer that does not honor it")
		assert.Contains(t, res.Content[0].Text, "dsm", "the refusal must name the algorithm")
		assert.Contains(t, res.Content[0].Text, "path_prefix")
		assert.Contains(t, res.Content[0].Text, corpusscan.AnalyzerName,
			"the refusal must list the honoring analyzers so the caller can act on it")
	})

	t.Run("an unknown algorithm still reports unknown analyzer", func(t *testing.T) {
		// Placement control: the refusal runs AFTER foundation.Get, so a
		// path_prefix carried by a name that does not exist must complain about
		// the NAME, not about the param.
		deps := &repoTestDeps{rootDir: t.TempDir(), gc: &fakeGraphCaller{}}
		handled, res := InterceptTopology(context.Background(), deps,
			kgtools.CallToolParams{Name: "query", Arguments: json.RawMessage(
				`{"mode":"topology","algorithm":"no_such_analyzer","graph":"knowledge","path_prefix":"cmd"}`)})
		require.True(t, handled)
		require.True(t, res.IsError)
		assert.Contains(t, res.Content[0].Text, "unknown analyzer")
	})
}

// TestInterceptTopology_BranchRefusedLoudly asserts branch is now a LOUD
// refusal rather than a silent drop: the call is claimed, errors naming the
// param, and issues ZERO reads because accountQueryParams runs before any
// dispatch. The known-positive control is the SAME call without branch, which
// must be served AND must drive a read — without it, "zero Execute calls" is
// equally satisfied by an arm that reads nothing at all.
func TestInterceptTopology_BranchRefusedLoudly(t *testing.T) {
	refused := &fakeGraphCaller{}
	deps := &repoTestDeps{rootDir: t.TempDir(), gc: refused}
	handled, res := InterceptTopology(context.Background(), deps,
		kgtools.CallToolParams{Name: "query", Arguments: json.RawMessage(
			`{"mode":"topology","algorithm":"` + qpTopologyAnalyzer + `","graph":"knowledge","branch":"some-branch"}`)})
	require.True(t, handled, "the call is claimed and refused, not passed through")
	require.True(t, res.IsError)
	assert.Contains(t, res.Content[0].Text, "branch", "the refusal must name the param")
	assert.Empty(t, refused.execRequests, "the refusal is pre-read: no Execute may be issued")

	served := &fakeGraphCaller{}
	depsOK := &repoTestDeps{rootDir: t.TempDir(), gc: served}
	handled, res = InterceptTopology(context.Background(), depsOK,
		kgtools.CallToolParams{Name: "query", Arguments: json.RawMessage(
			`{"mode":"topology","algorithm":"` + qpTopologyAnalyzer + `","graph":"knowledge"}`)})
	require.True(t, handled)
	require.False(t, res.IsError, "the same call without branch must be served: %s", res.Content[0].Text)
	assert.NotEmpty(t, served.execRequests, "the control must drive a real read, else the zero above proves nothing")
}

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

// TestInterceptTopology_DeadCode_NoRepo_Errors verifies that without an
// explicit repo: the intercept errors out before any wire activity (repo is
// required; it is never inferred from cwd).
func TestInterceptTopology_DeadCode_NoRepo_Errors(t *testing.T) {
	deps := &repoTestDeps{rootDir: t.TempDir()}
	handled, res := InterceptTopology(context.Background(), deps,
		kgtools.CallToolParams{Name: "query", Arguments: json.RawMessage(`{"mode":"topology","algorithm":"dead_code","graph":"code"}`)})
	assert.True(t, handled)
	require.True(t, res.IsError)
	assert.Contains(t, res.Content[0].Text, "repo is required")
}

// TestInterceptTopology_DeadCode_NonGoRepo_StatesTheIncompleteAnalysis verifies
// the intercept relays the analyzer's STATED inability for a non-Go tree.
//
// It used to assert the empty JSON array. That assertion is retired with the
// behavior it described: an empty finding set renders identically to "this repo
// has no dead code", so the run now reports one informational finding instead.
// The test still drives the same input — a tree runRTA cannot load — and still
// requires a non-error result; only what a non-error result CONTAINS changed.
func TestInterceptTopology_DeadCode_NonGoRepo_StatesTheIncompleteAnalysis(t *testing.T) {
	// Empty temp dir: no go.mod, no .go files. runRTA returns a diagnostic
	// ("no packages found" or "packages.Load failed").
	m := withTestManifest(t)
	dir := t.TempDir()
	// The dispatcher resolves the dead_code walk root from the repo argument
	// now, so the name has to name a real tree — here, the empty one under test.
	require.NoError(t, m.Record("knowledge", dir))
	deps := &repoTestDeps{
		rootDir: t.TempDir(),
		// GraphCaller must be non-nil so the intercept gets past the
		// "graph caller unavailable" guard. runRTA aborts before any
		// gc.Call is issued in this test, so the fake's Call body never
		// fires — fakeGraphCaller's default branch returns mutateResult.
		gc: &fakeGraphCaller{},
	}
	handled, res := InterceptTopology(context.Background(), deps,
		kgtools.CallToolParams{Name: "query", Arguments: json.RawMessage(`{"mode":"topology","algorithm":"dead_code","graph":"code","repo":"knowledge"}`)})
	assert.True(t, handled)
	require.False(t, res.IsError, "a tree the analysis cannot load is a stated answer, not an error: %s", res.Content[0].Text)
	body := res.Content[0].Text
	assert.Contains(t, body, clienttopo.DeadCodeIncompleteTitle,
		"the rendered body must carry the did-not-complete disclosure rather than an empty array")
	assert.NotContains(t, []string{"null", "[]"}, body,
		"an empty render is what made an unanalyzable tree indistinguishable from a clean one")
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
