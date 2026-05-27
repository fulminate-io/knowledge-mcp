// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeOpenSearchDomain(t *testing.T) {
	got := summarizeOpenSearchDomain(cloud.ResourceSpec{
		Name: "logs", Region: "us-east-1",
		Metadata: map[string]string{"engine_version": "OpenSearch_2.11", "instance_type": "t3.small.search", "instance_count": "3"},
	})
	assert.Contains(t, got, "OpenSearch domain logs")
	assert.Contains(t, got, "version=OpenSearch_2.11")
	assert.Contains(t, got, "type=t3.small.search")
	assert.Contains(t, got, "nodes=3")
}

func TestSummarizeOpenSearchDomain_EmptyMeta(t *testing.T) {
	assert.Equal(t, "OpenSearch domain x", summarizeOpenSearchDomain(cloud.ResourceSpec{Name: "x"}))
}
