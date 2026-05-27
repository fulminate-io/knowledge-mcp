// SPDX-License-Identifier: Apache-2.0

package gitlab

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cicd"
)

func TestGLGenericSummary_Project(t *testing.T) {
	got := glGenericSummary("Thing", cicd.ResourceSpec{
		Name: "x", Metadata: map[string]string{"project": "g/p"},
	})
	assert.Equal(t, "Thing x (g/p)", got)
}

func TestGLGenericSummary_Group(t *testing.T) {
	got := glGenericSummary("Thing", cicd.ResourceSpec{
		Name: "x", Metadata: map[string]string{"group": "g"},
	})
	assert.Equal(t, "Thing x (g)", got)
}

func TestGLGenericSummary_Bare(t *testing.T) {
	assert.Equal(t, "Thing x", glGenericSummary("Thing", cicd.ResourceSpec{Name: "x"}))
}
