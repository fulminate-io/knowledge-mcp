// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeRDSInstance(t *testing.T) {
	got := summarizeRDSInstance(cloud.ResourceSpec{
		Name: "db-1", Region: "us-east-1",
		Metadata: map[string]string{
			"engine": "postgres", "engine_version": "14", "instance_class": "db.m5.large",
			"multi_az": "true", "status": "available",
		},
	})
	assert.Contains(t, got, "RDS instance db-1")
	assert.Contains(t, got, "engine=postgres/14")
	assert.Contains(t, got, "class=db.m5.large")
	assert.Contains(t, got, "multi_az")
	assert.Contains(t, got, "status=available")
}

func TestSummarizeRDSInstance_EmptyMeta(t *testing.T) {
	assert.Equal(t, "RDS instance x", summarizeRDSInstance(cloud.ResourceSpec{Name: "x"}))
}
