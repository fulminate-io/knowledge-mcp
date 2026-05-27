// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeCloudFrontDistribution(t *testing.T) {
	got := summarizeCloudFrontDistribution(cloud.ResourceSpec{
		Name: "E1234",
		Metadata: map[string]string{
			"enabled":     "true",
			"status":      "Deployed",
			"price_class": "PriceClass_100",
		},
	})
	assert.Contains(t, got, "CloudFront distribution E1234")
	assert.Contains(t, got, "enabled=true")
	assert.Contains(t, got, "status=Deployed")
	assert.Contains(t, got, "price=PriceClass_100")
}

func TestSummarizeCloudFrontDistribution_EmptyMeta(t *testing.T) {
	assert.Equal(t, "CloudFront distribution x", summarizeCloudFrontDistribution(cloud.ResourceSpec{Name: "x"}))
}
