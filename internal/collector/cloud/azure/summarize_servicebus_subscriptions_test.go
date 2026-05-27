// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeServiceBusSubscription(t *testing.T) {
	assert.Equal(t, "Service Bus subscription s", summarizeServiceBusSubscription(cloud.ResourceSpec{Name: "s"}))
}
