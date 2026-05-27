// SPDX-License-Identifier: Apache-2.0

package gitlab

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cicd"
)

func TestSummarizeGitLabOIDCIssuer(t *testing.T) {
	got := summarizeGitLabOIDCIssuer(cicd.ResourceSpec{
		Name:     "GitLab OIDC (g)",
		Metadata: map[string]string{"issuer": "https://gitlab.example.com", "group": "g"},
	})
	assert.Contains(t, got, "GitLab OIDC issuer GitLab OIDC (g)")
	assert.Contains(t, got, "issuer=https://gitlab.example.com")
	assert.Contains(t, got, "(g)")
}
