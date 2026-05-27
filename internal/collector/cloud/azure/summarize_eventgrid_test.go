// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeEventGridTopic(t *testing.T) {
	assert.Equal(t, "Event Grid topic t", summarizeEventGridTopic(cloud.ResourceSpec{Name: "t"}))
}

func TestSummarizeEventGridSubscription(t *testing.T) {
	assert.Equal(t, "Event Grid subscription s", summarizeEventGridSubscription(cloud.ResourceSpec{Name: "s"}))
}
