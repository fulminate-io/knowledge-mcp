// SPDX-License-Identifier: Apache-2.0

package bitbucket

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cicd"
)

func TestBBGenericSummary_WorkspaceRepo(t *testing.T) {
	got := bbGenericSummary("Thing", cicd.ResourceSpec{
		Name: "x", Metadata: map[string]string{"workspace": "ws", "repo": "r"},
	})
	assert.Equal(t, "Thing x (ws/r)", got)
}

func TestBBGenericSummary_WorkspaceOnly(t *testing.T) {
	got := bbGenericSummary("Thing", cicd.ResourceSpec{
		Name: "x", Metadata: map[string]string{"workspace": "ws"},
	})
	assert.Equal(t, "Thing x (ws)", got)
}

func TestBBGenericSummary_Bare(t *testing.T) {
	assert.Equal(t, "Thing x", bbGenericSummary("Thing", cicd.ResourceSpec{Name: "x"}))
}
