// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeSESIdentity(t *testing.T) {
	got := summarizeSESIdentity(cloud.ResourceSpec{
		Name: "noreply@example.com", Region: "us-east-1",
		Metadata: map[string]string{"identity_type": "EMAIL_ADDRESS", "verified_for_sending": "true"},
	})
	assert.Contains(t, got, "SES identity noreply@example.com")
	assert.Contains(t, got, "type=EMAIL_ADDRESS")
	assert.Contains(t, got, "verified")
}

func TestSummarizeSESIdentity_EmptyMeta(t *testing.T) {
	assert.Equal(t, "SES identity x", summarizeSESIdentity(cloud.ResourceSpec{Name: "x"}))
}
