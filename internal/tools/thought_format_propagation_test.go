// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	clientthought "github.com/fulminate-io/knowledge-mcp/internal/thought"
)

// TestFormatPropagationResult (FAILS-WHEN-ABSENT) asserts the propagate render
// reports convergence PER COMPONENT — "N of M components converged" — lists each
// non-converged component's size + residual delta, summarizes omitted ones, and
// NEVER emits a bare global "converged=false". Goes red if the per-component line
// regresses to the global flag.
func TestFormatPropagationResult(t *testing.T) {
	r := clientthought.PropagationResult{
		ThoughtsProcessed:   100,
		Components:          12,
		Iterations:          240,
		ComponentsConverged: 10,
		NonConverged: []clientthought.NonConvergedComponent{
			{Size: 160, ValenceResidual: 0.42, MagnitudeResidual: 0.10},
			{Size: 33, ValenceResidual: 0.08, MagnitudeResidual: 0.21},
		},
		NonConvergedOmitted: 3,
		Converged:           false,
	}
	out := formatPropagationResult(r)

	assert.Contains(t, out, "10 of 12 components converged",
		"render shows the per-component converged count")
	assert.Contains(t, out, "size 160", "render lists the non-converged component size")
	assert.Contains(t, out, "Δ=0.4200", "render lists the non-converged valence residual delta")
	assert.Contains(t, out, "and 3 more", "render summarizes the omitted non-converged components")
	assert.NotContains(t, strings.ToLower(out), "converged=false",
		"render must NEVER emit the bare global converged=false flag")

	// All-converged case: clean per-component line, no non-converged detail.
	allConv := clientthought.PropagationResult{
		ThoughtsProcessed: 50, Components: 5, Iterations: 20,
		ComponentsConverged: 5, Converged: true,
	}
	outAll := formatPropagationResult(allConv)
	assert.Contains(t, outAll, "5 of 5 components converged")
	assert.NotContains(t, outAll, "non-converged")
}
