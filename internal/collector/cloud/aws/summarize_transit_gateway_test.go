// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeTransitGateway(t *testing.T) {
	got := summarizeTransitGateway(cloud.ResourceSpec{
		Name: "tgw-1", Region: "us-east-1",
		Metadata: map[string]string{"state": "available", "amazon_side_asn": "64512"},
	})
	assert.Contains(t, got, "transit gateway tgw-1")
	assert.Contains(t, got, "state=available")
	assert.Contains(t, got, "asn=64512")
}

func TestSummarizeTransitGateway_EmptyMeta(t *testing.T) {
	assert.Equal(t, "transit gateway x", summarizeTransitGateway(cloud.ResourceSpec{Name: "x"}))
}

func TestSummarizeTransitGatewayAttachment(t *testing.T) {
	got := summarizeTransitGatewayAttachment(cloud.ResourceSpec{
		Name: "tgw-att-1", Region: "us-east-1",
		Metadata: map[string]string{"resource_type": "vpc", "resource_id": "vpc-abc", "state": "available"},
	})
	assert.Contains(t, got, "transit gateway attachment tgw-att-1")
	assert.Contains(t, got, "type=vpc")
	assert.Contains(t, got, "for=vpc-abc")
	assert.Contains(t, got, "state=available")
}

func TestSummarizeTransitGatewayAttachment_EmptyMeta(t *testing.T) {
	assert.Equal(t, "transit gateway attachment x", summarizeTransitGatewayAttachment(cloud.ResourceSpec{Name: "x"}))
}
