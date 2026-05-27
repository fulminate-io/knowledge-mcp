// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeComputeNetwork(t *testing.T) {
	assert.Equal(t, "VPC network n", summarizeComputeNetwork(cloud.ResourceSpec{Name: "n"}))
}

func TestSummarizeComputeSubnetwork(t *testing.T) {
	assert.Equal(t, "subnetwork s", summarizeComputeSubnetwork(cloud.ResourceSpec{Name: "s"}))
}

func TestSummarizeComputeFirewall(t *testing.T) {
	assert.Equal(t, "firewall rule f", summarizeComputeFirewall(cloud.ResourceSpec{Name: "f"}))
}
