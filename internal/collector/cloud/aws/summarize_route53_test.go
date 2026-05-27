// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeRoute53HostedZone(t *testing.T) {
	got := summarizeRoute53HostedZone(cloud.ResourceSpec{
		Name:     "example.com",
		Metadata: map[string]string{"private_zone": "true", "resource_record_set_count": "10"},
	})
	assert.Contains(t, got, "Route53 hosted zone example.com")
	assert.Contains(t, got, "private")
	assert.Contains(t, got, "records=10")
}

func TestSummarizeRoute53HostedZone_EmptyMeta(t *testing.T) {
	assert.Equal(t, "Route53 hosted zone x", summarizeRoute53HostedZone(cloud.ResourceSpec{Name: "x"}))
}
