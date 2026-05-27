// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeServiceBusNamespace(t *testing.T) {
	assert.Equal(t, "Service Bus namespace n", summarizeServiceBusNamespace(cloud.ResourceSpec{Name: "n"}))
}

func TestSummarizeServiceBusQueue(t *testing.T) {
	assert.Equal(t, "Service Bus queue q", summarizeServiceBusQueue(cloud.ResourceSpec{Name: "q"}))
}

func TestSummarizeServiceBusTopic(t *testing.T) {
	assert.Equal(t, "Service Bus topic t", summarizeServiceBusTopic(cloud.ResourceSpec{Name: "t"}))
}
