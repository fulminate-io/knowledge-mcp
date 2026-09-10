// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/transcriptanalytics"
)

// TestAnalyzeUsageSchema_DeclaresScopeSelector pins the wire surface of the scope selector.
//
// The scope enum is asserted as an EXACT set rather than by containment: a stray fifth
// value, or a renamed one, is precisely the drift that makes a schema advertise something
// the validator rejects, and containment would not catch either.
func TestAnalyzeUsageSchema_DeclaresScopeSelector(t *testing.T) {
	def := AnalyzeUsageToolDef()
	props := def.InputSchema.Properties

	t.Run("scope enum is exactly the four values", func(t *testing.T) {
		scope, ok := props["scope"]
		require.True(t, ok, "the schema declares a scope property")
		assert.Equal(t, []string{"all", "session-tree", "single", "time-range"}, scope.Enum)
		assert.Equal(t, transcriptanalytics.ScopeValues(), scope.Enum,
			"and it reads from the analyzer's single vocabulary declaration, not a second hand-kept list")
	})

	t.Run("every selector property is declared and described", func(t *testing.T) {
		for _, name := range []string{"scope", "session", "agent", "since", "until"} {
			p, ok := props[name]
			require.True(t, ok, "property %q is declared", name)
			assert.Equal(t, "string", p.Type, "property %q", name)
			assert.NotEmpty(t, p.Description, "property %q carries a description", name)
		}
	})

	t.Run("the agent property documents the name form and how it resolves", func(t *testing.T) {
		agent := props["agent"].Description
		assert.Contains(t, agent, "name", "an operator holds the spawn name, so the schema must say a name is accepted")
		assert.Contains(t, agent, "id", "and that the cache lane id still is")
		assert.Contains(t, agent, "resolved against the cache within session when one is given",
			"the schema must state HOW a name resolves, not merely mention session — a one-line "+
				"description carrying the four words and no rule would otherwise keep this green")
		assert.Contains(t, agent, "ambiguous", "and what happens when a name matches more than one lane")
		assert.Contains(t, props["session"].Description, "Session id", "session keeps its own meaning")
	})

	t.Run("the scope property no longer refuses a session beside an agent", func(t *testing.T) {
		scope := props["scope"].Description
		assert.NotContains(t, scope, "requires exactly one of session or agent",
			"single now admits the pair for a lane name; the stale clause tells every LLM caller the opposite")
		assert.Contains(t, scope, "name",
			"and the replacement says what single accepts instead of leaving the rule unstated")
	})

	t.Run("the tool description narrows the cold-cache note and keeps the corpus paragraph", func(t *testing.T) {
		d := AnalyzeUsageToolDef().Description
		assert.Contains(t, d, "--seed", "the hint's own advice stays discoverable from the tool doc")
		assert.Contains(t, d, "holds no lanes",
			"the hint now means an empty cache specifically, and the doc must say so or it understates the behaviour")
		assert.Contains(t, d, "RETAINS",
			"the corpus-retention explanation is load-bearing and no test elsewhere covers it")
	})

	t.Run("the operation enum is unchanged", func(t *testing.T) {
		op, ok := props["operation"]
		require.True(t, ok)
		assert.Equal(t, []string{"run-detectors", "recommend"}, op.Enum,
			"the scope selector REPLACED a per-agent operation; a third operation reappearing here is the regression")
	})

	t.Run("an undeclared parameter is still refused", func(t *testing.T) {
		deps := analyzeUsageTestDeps{analyzer: fakeUsageAnalyzer{report: nonEmptyReportForRecommend()}}
		handled, body, isErr := callAnalyzeUsage(t, deps, `{"operation":"run-detectors","scopes":"all"}`)
		require.True(t, handled)
		assert.True(t, isErr, "widening the schema by five keys must not open the surface")
		assert.Contains(t, body, "scopes")
	})

	t.Run("a malformed since is a named error, never a zero time", func(t *testing.T) {
		deps := analyzeUsageTestDeps{analyzer: fakeUsageAnalyzer{report: nonEmptyReportForRecommend()}}
		handled, body, isErr := callAnalyzeUsage(t, deps, `{"operation":"run-detectors","scope":"time-range","since":"yesterday"}`)
		require.True(t, handled)
		require.True(t, isErr, "a zero time would leave the bound unset and silently widen the population")
		assert.Contains(t, body, "since", "the error names the offending field")
		assert.Contains(t, body, "RFC3339", "and the expected format")

		// The known-positive on the same probe: a WELL-FORMED timestamp is accepted.
		handled, okBody, isErr := callAnalyzeUsage(t, deps, `{"operation":"run-detectors","scope":"time-range","since":"2026-08-29T00:00:00Z"}`)
		require.True(t, handled)
		assert.False(t, isErr, "a valid RFC3339 bound is accepted; got %s", okBody)
	})

	t.Run("an invalid scope-and-selector combination is refused with the vocabulary", func(t *testing.T) {
		deps := analyzeUsageTestDeps{analyzer: fakeUsageAnalyzer{report: nonEmptyReportForRecommend()}}
		handled, body, isErr := callAnalyzeUsage(t, deps, `{"operation":"recommend","scope":"single"}`)
		require.True(t, handled)
		require.True(t, isErr, "single with neither session nor agent selects no population")
		assert.True(t, strings.Contains(body, "session") && strings.Contains(body, "agent"),
			"the validator's own message reaches the caller; got %s", body)
	})
}
