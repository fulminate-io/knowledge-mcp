// SPDX-License-Identifier: Apache-2.0

package rerank

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pipeline_parse_test.go covers the parse-time / validation-time surface
// of the rerank pipeline DSL. It is deliberately only that half: the
// dispatch-guard half was once a separate server-side test file, kept apart
// because it reached store-unexported identifiers this package cannot see,
// and both that file and the executeSearch dispatch it guarded have since
// been removed from the tree. Shared fixture helpers (rawJSON, fixtureNode,
// resultSpec, makeResults) live in pipeline_test.go and are used here by
// package proximity.

// TestParsePipeline_Errors covers the ParsePipeline + Validate diagnostics
// the user-facing error path. Unknown-op, missing-op, limit-in-pre, bad
// regex, $query in regex value, depth>3, mutual exclusion.
func TestParsePipeline_Errors(t *testing.T) {
	t.Run("unknown op names phase + index", func(t *testing.T) {
		raw := []byte(`{"pre":[{"op":"frob"}]}`)
		_, err := ParsePipeline(raw)
		require.Error(t, err)
		assert.Contains(t, err.Error(), `pre[0]`)
		assert.Contains(t, err.Error(), `unknown op "frob"`)
	})

	t.Run("missing op discriminator field", func(t *testing.T) {
		raw := []byte(`{"pre":[{"action":"drop"}]}`)
		_, err := ParsePipeline(raw)
		require.Error(t, err)
		assert.Contains(t, err.Error(), `op missing required "op" discriminator field`)
	})

	t.Run("limit op rejected in pre", func(t *testing.T) {
		raw := []byte(`{"pre":[{"op":"limit","n":5}]}`)
		_, err := ParsePipeline(raw)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "pre[0]")
		assert.Contains(t, err.Error(), "limit op not allowed in pre")
	})

	t.Run("bad regex pattern rejected at Validate", func(t *testing.T) {
		raw := []byte(`{"post":[{"op":"filter","action":"drop","where":{"field":"summary","match":"regex","value":"["}}]}`)
		_, err := ParsePipeline(raw)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "regex")
	})

	t.Run("$query forbidden in regex value", func(t *testing.T) {
		raw := []byte(`{"post":[{"op":"filter","action":"drop","where":{"field":"summary","match":"regex","value":"$query"}}]}`)
		_, err := ParsePipeline(raw)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "$query")
	})

	t.Run("depth > 3 rejected at Validate", func(t *testing.T) {
		// any/any/any/any-leaf — depth 4 must reject.
		raw := []byte(`{"post":[{"op":"filter","action":"drop","where":` +
			`{"any":[{"any":[{"any":[{"any":[` +
			`{"field":"type","match":"equals","value":"function"}` +
			`]}]}]}]}` +
			`}]}`)
		_, err := ParsePipeline(raw)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "depth")
	})

	t.Run("mutual exclusion violation at Validate", func(t *testing.T) {
		// Leaf field + any siblings → invalid.
		raw := []byte(`{"post":[{"op":"filter","action":"drop","where":` +
			`{"field":"type","match":"equals","value":"function",` +
			`"any":[{"field":"type","match":"equals","value":"method"}]}` +
			`}]}`)
		_, err := ParsePipeline(raw)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "cannot mix")
	})
}

// TestParsePipeline_Happy confirms the end-to-end Parse path and the
// UnmarshalJSON delegation: building a Pipeline by Unmarshal must produce
// the same shape and validation behavior as ParsePipeline.
func TestParsePipeline_Happy(t *testing.T) {
	raw := []byte(`{
		"pre":  [{"op":"filter","action":"drop","where":{"field":"file_path","match":"suffix","value":"_test.go"}}],
		"post": [{"op":"score","mode":"multiply","value":0.8,"where":{"field":"type","match":"equals","value":"function"}},
		         {"op":"limit","n":20}]
	}`)

	t.Run("ParsePipeline round-trips", func(t *testing.T) {
		p, err := ParsePipeline(raw)
		require.NoError(t, err)
		require.NotNil(t, p)
		require.Len(t, p.Pre, 1)
		require.Len(t, p.Post, 2)
		assert.Equal(t, "filter", p.Pre[0].Name())
		assert.Equal(t, "score", p.Post[0].Name())
		assert.Equal(t, "limit", p.Post[1].Name())
	})

	t.Run("UnmarshalJSON delegates to ParsePipeline", func(t *testing.T) {
		var p Pipeline
		require.NoError(t, json.Unmarshal(raw, &p))
		require.Len(t, p.Pre, 1)
		require.Len(t, p.Post, 2)

		// A malformed pipeline through Unmarshal must surface the same
		// error class as direct ParsePipeline.
		bad := []byte(`{"pre":[{"op":"frob"}]}`)
		var p2 Pipeline
		err := json.Unmarshal(bad, &p2)
		require.Error(t, err)
		assert.Contains(t, err.Error(), `pre[0]`)
	})
}

// TestParsePipeline_IsTestTestKindFields confirms the closed-set field
// validator accepts the new is_test / test_kind predicate fields. Without
// the validPredicateField extension, ParsePipeline would reject these
// pipelines with "unknown field". Mirrors the TestParsePipeline_Happy
// shape — the assertion is "no error", not a behavioral round-trip
// (predicate evaluation is exercised in TestPredicate_IsTestTestKind).
func TestParsePipeline_IsTestTestKindFields(t *testing.T) {
	t.Run("test_kind in [...] parses + validates", func(t *testing.T) {
		raw := []byte(`{"pre":[{"op":"filter","action":"drop","where":` +
			`{"field":"test_kind","match":"in","value":["test","benchmark"]}}]}`)
		p, err := ParsePipeline(raw)
		require.NoError(t, err)
		require.NotNil(t, p)
		require.Len(t, p.Pre, 1)
	})

	t.Run("is_test equals true parses + validates", func(t *testing.T) {
		raw := []byte(`{"post":[{"op":"filter","action":"drop","where":` +
			`{"field":"is_test","match":"equals","value":"true"}}]}`)
		p, err := ParsePipeline(raw)
		require.NoError(t, err)
		require.NotNil(t, p)
		require.Len(t, p.Post, 1)
	})
}
