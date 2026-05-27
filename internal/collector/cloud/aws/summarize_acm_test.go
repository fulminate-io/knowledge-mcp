// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeACMCertificate(t *testing.T) {
	got := summarizeACMCertificate(cloud.ResourceSpec{
		Name:   "example.com",
		Region: "us-east-1",
		Metadata: map[string]string{
			"domain_name": "example.com",
			"status":      "ISSUED",
			"type":        "AMAZON_ISSUED",
			"not_after":   "2026-01-01T00:00:00Z",
		},
	})
	assert.Contains(t, got, "ACM cert example.com")
	assert.Contains(t, got, "status=ISSUED")
	assert.Contains(t, got, "type=AMAZON_ISSUED")
	assert.Contains(t, got, "expires=2026-01-01T00:00:00Z")
	assert.Contains(t, got, "in us-east-1")
}

func TestSummarizeACMCertificate_EmptyMeta(t *testing.T) {
	got := summarizeACMCertificate(cloud.ResourceSpec{Name: "x"})
	assert.Equal(t, "ACM cert x", got)
}
