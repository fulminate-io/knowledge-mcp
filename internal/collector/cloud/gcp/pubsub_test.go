// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPubSubTopicsSubCollector_Name(t *testing.T) {
	c := &pubsubTopicsSubCollector{}
	assert.Equal(t, "gcp-pubsub-topics", c.Name())
}

func TestPubSubSubscriptionsSubCollector_Name(t *testing.T) {
	c := &pubsubSubscriptionsSubCollector{}
	assert.Equal(t, "gcp-pubsub-subscriptions", c.Name())
}

// NOTE: Dead letter edge type (EdgeDeadLettersTo) and Pub/Sub ENCRYPTS_WITH
// are tested via integration since they require topic.Config(ctx) and
// sub.Config(ctx) RPCs. The edge type constants and field assignments are
// verified by the compiler and the Phase 6 build step.
