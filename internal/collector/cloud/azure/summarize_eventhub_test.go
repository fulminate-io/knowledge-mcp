// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeEventHubNamespace(t *testing.T) {
	assert.Equal(t, "Event Hub namespace n", summarizeEventHubNamespace(cloud.ResourceSpec{Name: "n"}))
}

func TestSummarizeEventHub(t *testing.T) {
	assert.Equal(t, "Event Hub h", summarizeEventHub(cloud.ResourceSpec{Name: "h"}))
}

func TestSummarizeEventHubConsumerGroup(t *testing.T) {
	assert.Equal(t, "Event Hub consumer group cg", summarizeEventHubConsumerGroup(cloud.ResourceSpec{Name: "cg"}))
}
