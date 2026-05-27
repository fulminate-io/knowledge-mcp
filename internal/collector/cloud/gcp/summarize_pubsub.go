// SPDX-License-Identifier: Apache-2.0

package gcp

import "github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"

func init() {
	cloud.Register("gcp:pubsub:topic", summarizePubSubTopic)
	cloud.Register("gcp:pubsub:subscription", summarizePubSubSubscription)
}

func summarizePubSubTopic(spec cloud.ResourceSpec) string {
	return gcpGenericSummary("Pub/Sub topic", spec)
}

func summarizePubSubSubscription(spec cloud.ResourceSpec) string {
	return gcpGenericSummary("Pub/Sub subscription", spec)
}
