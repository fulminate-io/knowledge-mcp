// SPDX-License-Identifier: Apache-2.0

package externalcollector

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// TestValidateParams covers the criterion schema {repo:{string,required},
// depth:{int}}: it accepts {repo:x} and {repo:x,depth:2}, rejects {} (missing
// required repo), and rejects {repo:x,bogus:1} (unknown param).
func TestValidateParams(t *testing.T) {
	schema := map[string]*knowledgev1.ParamSpec{
		"repo":  {Type: "string", Required: true},
		"depth": {Type: "int"},
	}

	t.Run("accepts required only", func(t *testing.T) {
		require.NoError(t, ValidateParams(schema, map[string]any{"repo": "x"}))
	})

	t.Run("accepts required plus optional", func(t *testing.T) {
		// depth 2 as float64 mirrors a JSON-decoded number.
		require.NoError(t, ValidateParams(schema, map[string]any{"repo": "x", "depth": float64(2)}))
	})

	t.Run("rejects missing required", func(t *testing.T) {
		err := ValidateParams(schema, map[string]any{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "repo")
	})

	t.Run("rejects unknown param", func(t *testing.T) {
		err := ValidateParams(schema, map[string]any{"repo": "x", "bogus": float64(1)})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "bogus")
	})

	t.Run("rejects wrong type", func(t *testing.T) {
		err := ValidateParams(schema, map[string]any{"repo": float64(7)})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "repo")
	})
}
