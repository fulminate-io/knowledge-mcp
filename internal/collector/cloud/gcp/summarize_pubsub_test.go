// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizePubSubTopic(t *testing.T) {
	assert.Equal(t, "Pub/Sub topic t", summarizePubSubTopic(cloud.ResourceSpec{Name: "t"}))
}

func TestSummarizePubSubSubscription(t *testing.T) {
	assert.Equal(t, "Pub/Sub subscription s", summarizePubSubSubscription(cloud.ResourceSpec{Name: "s"}))
}
