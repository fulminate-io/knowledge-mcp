// SPDX-License-Identifier: Apache-2.0

package rerank

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// default_pipeline_test.go owns the pure-structural unit test for the
// intent-aware default Post pipeline. Integration tests
// (AppliedToCodeGraphOnly, IntentClassifiedRouting) live in
// integration_test.go in this directory (package rerank_test). The
// external-test-package pattern is required because the integration
// tests import both store and rerank — running them inside package
// rerank would create an import cycle (rerank imports store, store
// can't reciprocate without breaking the one-way dep direction).
//
// TestDefaultRerankPipeline_StructureLocked — pure structural unit test
// asserting each Intent branch's op count, predicate shape, sign of
// magnitudes, the helper/mock/fixture neutrality rule, and the
// same-pointer-on-repeated-calls invariant (T3-B verification of the
// package-init constants).

// TestDefaultRerankPipeline_StructureLocked asserts the locked per-intent
// Post pipeline structure: op count, predicate field/match, sign of
// magnitudes, neutrality of helper/mock/fixture, and the package-init
// same-pointer invariant.
func TestDefaultRerankPipeline_StructureLocked(t *testing.T) {
	t.Run("IntentImpl has 3 Post ops with correct signs and predicates", func(t *testing.T) {
		p := defaultRerankPipeline(IntentImpl)
		require.NotNil(t, p)
		assert.Empty(t, p.Pre, "default impl pipeline has no Pre phase")
		require.Len(t, p.Post, 3, "impl branch: strong-demote + mild-demote + non-test-promote")

		// Op 0: demote strong test kinds.
		sop, ok := p.Post[0].(*ScoreOp)
		require.True(t, ok, "Post[0] must be a *ScoreOp")
		assert.Equal(t, "score", sop.Op)
		assert.Equal(t, "add", sop.Mode)
		assert.Equal(t, "test_kind", sop.Where.Field)
		assert.Equal(t, "in", sop.Where.Match)
		var kinds []string
		require.NoError(t, json.Unmarshal(sop.Where.Value, &kinds))
		assert.ElementsMatch(t, []string{"test", "benchmark", "example", "fuzz"}, kinds,
			"strong-demote set must NOT include helper/mock/fixture — neutrality rule")
		assert.Less(t, sop.Value, 0.0, "impl-strong is a demote (negative add)")

		// Op 1: mild-demote setup/teardown.
		sop1, ok := p.Post[1].(*ScoreOp)
		require.True(t, ok)
		assert.Equal(t, "in", sop1.Where.Match)
		require.NoError(t, json.Unmarshal(sop1.Where.Value, &kinds))
		assert.ElementsMatch(t, []string{"setup", "teardown"}, kinds)
		assert.Less(t, sop1.Value, 0.0, "impl-mild is a demote")
		assert.Greater(t, sop1.Value, sop.Value, "mild magnitude must be smaller than strong")

		// Op 2: promote non-test (TestKind=="").
		sop2, ok := p.Post[2].(*ScoreOp)
		require.True(t, ok)
		assert.Equal(t, "test_kind", sop2.Where.Field)
		assert.Equal(t, "equals", sop2.Where.Match)
		var nonTest string
		require.NoError(t, json.Unmarshal(sop2.Where.Value, &nonTest))
		assert.Empty(t, nonTest, "non-test predicate must use empty-string TestKind")
		assert.Greater(t, sop2.Value, 0.0, "impl: non-test code is promoted")
	})

	t.Run("IntentTest has 3 Post ops with mirrored signs", func(t *testing.T) {
		p := defaultRerankPipeline(IntentTest)
		require.NotNil(t, p)
		require.Len(t, p.Post, 3)

		sop, ok := p.Post[0].(*ScoreOp)
		require.True(t, ok)
		assert.Greater(t, sop.Value, 0.0, "test-strong is a promote (positive add)")

		sop1, ok := p.Post[1].(*ScoreOp)
		require.True(t, ok)
		assert.Greater(t, sop1.Value, 0.0, "test-mild on setup/teardown is a promote")
		assert.Less(t, sop1.Value, sop.Value, "mild magnitude must be smaller than strong")

		sop2, ok := p.Post[2].(*ScoreOp)
		require.True(t, ok)
		assert.Less(t, sop2.Value, 0.0, "test: non-test code is demoted")
	})

	t.Run("IntentUnknown has 1 mild-demote Post op", func(t *testing.T) {
		p := defaultRerankPipeline(IntentUnknown)
		require.NotNil(t, p)
		require.Len(t, p.Post, 1, "unknown branch: single mild demote on strong kinds")

		sop, ok := p.Post[0].(*ScoreOp)
		require.True(t, ok)
		assert.Equal(t, "test_kind", sop.Where.Field)
		assert.Equal(t, "in", sop.Where.Match)
		assert.Less(t, sop.Value, 0.0, "unknown demote is negative")
		assert.Greater(t, sop.Value, -0.2, "unknown demote magnitude is mild (>-0.2)")
	})

	t.Run("Helper/mock/fixture kinds neutral on every intent", func(t *testing.T) {
		// Walk every ScoreOp's Where.Value across every intent. Any
		// predicate referencing helper/mock/fixture violates the
		// neutrality rule; flag it.
		forbidden := map[string]struct{}{"helper": {}, "mock": {}, "fixture": {}}
		for _, intent := range []Intent{IntentImpl, IntentTest, IntentUnknown} {
			p := defaultRerankPipeline(intent)
			for i, op := range p.Post {
				sop, ok := op.(*ScoreOp)
				if !ok {
					continue
				}
				if sop.Where.Match != "in" {
					continue
				}
				var kinds []string
				if err := json.Unmarshal(sop.Where.Value, &kinds); err != nil {
					continue
				}
				for _, k := range kinds {
					if _, bad := forbidden[k]; bad {
						t.Errorf("intent=%s op[%d] targets forbidden kind %q (neutrality rule)",
							intent, i, k)
					}
				}
			}
		}
	})

	t.Run("Same pointer returned on repeated calls (package-init invariant)", func(t *testing.T) {
		// T3-B: defaultRerankPipeline returns the SAME *Pipeline pointer
		// on repeated calls, proving the package-init constants are not
		// rebuilt per-call. Capture into separate vars so testifylint sees
		// distinct expressions (it flags `assert.Same(t, f(x), f(x))` as
		// useless even though the equality of two function-call results IS
		// what we want to assert here).
		implA, implB := defaultRerankPipeline(IntentImpl), defaultRerankPipeline(IntentImpl)
		testA, testB := defaultRerankPipeline(IntentTest), defaultRerankPipeline(IntentTest)
		unkA, unkB := defaultRerankPipeline(IntentUnknown), defaultRerankPipeline(IntentUnknown)
		assert.Same(t, implA, implB)
		assert.Same(t, testA, testB)
		assert.Same(t, unkA, unkB)
	})

	t.Run("Unrecognized intent returns nil", func(t *testing.T) {
		assert.Nil(t, defaultRerankPipeline(Intent("not-a-real-intent")))
	})
}
