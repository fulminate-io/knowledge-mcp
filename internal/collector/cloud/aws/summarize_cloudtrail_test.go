// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeCloudTrailTrail(t *testing.T) {
	got := summarizeCloudTrailTrail(cloud.ResourceSpec{
		Name: "audit-trail", Region: "us-east-1",
		Metadata: map[string]string{
			"multi_region":                "true",
			"home_region":                 "us-east-1",
			"log_file_validation_enabled": "true",
		},
	})
	assert.Contains(t, got, "CloudTrail trail audit-trail")
	assert.Contains(t, got, "multi-region")
	assert.Contains(t, got, "home=us-east-1")
	assert.Contains(t, got, "log-file-validation")
	assert.Contains(t, got, "in us-east-1")
}

func TestSummarizeCloudTrailTrail_EmptyMeta(t *testing.T) {
	assert.Equal(t, "CloudTrail trail x", summarizeCloudTrailTrail(cloud.ResourceSpec{Name: "x"}))
}
