// SPDX-License-Identifier: Apache-2.0

package github

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cicd"
)

func TestGHGenericSummary_OrgRepo(t *testing.T) {
	got := ghGenericSummary("Thing", cicd.ResourceSpec{
		Name: "x", Metadata: map[string]string{"org": "o", "repo": "o/r"},
	})
	assert.Equal(t, "Thing x (o/o/r)", got)
}

func TestGHGenericSummary_OrgOnly(t *testing.T) {
	got := ghGenericSummary("Thing", cicd.ResourceSpec{
		Name: "x", Metadata: map[string]string{"org": "o"},
	})
	assert.Equal(t, "Thing x (o)", got)
}

func TestGHGenericSummary_NoOrg(t *testing.T) {
	assert.Equal(t, "Thing x", ghGenericSummary("Thing", cicd.ResourceSpec{Name: "x"}))
}
