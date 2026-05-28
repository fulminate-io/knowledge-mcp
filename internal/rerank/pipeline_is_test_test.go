// SPDX-License-Identifier: Apache-2.0

package rerank

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
)

func TestPredicate_IsTestTestKind(t *testing.T) {
	// Three nodes covering: (a) prod code, (b) test, (c) benchmark.
	in := []engine.SearchResult{
		{Node: &knowledgev1.Node{Id: "prod", Type: "function", IsTest: false, TestKind: ""}, Score: 3.0},
		{Node: &knowledgev1.Node{Id: "test", Type: "function", IsTest: true, TestKind: "test"}, Score: 2.0},
		{Node: &knowledgev1.Node{Id: "bench", Type: "function", IsTest: true, TestKind: "benchmark"}, Score: 1.0},
	}

	t.Run("test_kind in [test,benchmark] drops both test variants", func(t *testing.T) {
		op := &FilterOp{
			Op:     "filter",
			Action: "drop",
			Where: Predicate{
				Field: "test_kind", Match: "in",
				Value: rawJSON(t, []string{"test", "benchmark"}),
			},
		}
		require.NoError(t, op.Validate())
		out, err := op.Apply("ignored", in)
		require.NoError(t, err)
		require.Len(t, out, 1)
		assert.Equal(t, "prod", out[0].Node.Id)
	})

	t.Run("test_kind equals benchmark drops only bench", func(t *testing.T) {
		op := &FilterOp{
			Op:     "filter",
			Action: "drop",
			Where: Predicate{
				Field: "test_kind", Match: "equals",
				Value: rawJSON(t, "benchmark"),
			},
		}
		require.NoError(t, op.Validate())
		out, err := op.Apply("ignored", in)
		require.NoError(t, err)
		require.Len(t, out, 2)
		assert.Equal(t, "prod", out[0].Node.Id)
		assert.Equal(t, "test", out[1].Node.Id)
	})

	t.Run("is_test equals true drops every test node", func(t *testing.T) {
		op := &FilterOp{
			Op:     "filter",
			Action: "drop",
			Where: Predicate{
				Field: "is_test", Match: "equals",
				Value: rawJSON(t, "true"),
			},
		}
		require.NoError(t, op.Validate())
		out, err := op.Apply("ignored", in)
		require.NoError(t, err)
		require.Len(t, out, 1)
		assert.Equal(t, "prod", out[0].Node.Id)
	})

	t.Run("is_test equals false drops only the prod node", func(t *testing.T) {
		op := &FilterOp{
			Op:     "filter",
			Action: "drop",
			Where: Predicate{
				Field: "is_test", Match: "equals",
				Value: rawJSON(t, "false"),
			},
		}
		require.NoError(t, op.Validate())
		out, err := op.Apply("ignored", in)
		require.NoError(t, err)
		require.Len(t, out, 2)
		assert.Equal(t, "test", out[0].Node.Id)
		assert.Equal(t, "bench", out[1].Node.Id)
	})

	t.Run("readPredicateField direct read returns expected strings", func(t *testing.T) {
		// is_test always returns "true" or "false" (bool false is meaningful).
		assert.Equal(t, "false", readPredicateField("is_test", &knowledgev1.Node{IsTest: false}))
		assert.Equal(t, "true", readPredicateField("is_test", &knowledgev1.Node{IsTest: true}))
		// test_kind passes the string through unchanged.
		assert.Empty(t, readPredicateField("test_kind", &knowledgev1.Node{}))
		assert.Equal(t, "benchmark", readPredicateField("test_kind", &knowledgev1.Node{TestKind: "benchmark"}))
	})
}
