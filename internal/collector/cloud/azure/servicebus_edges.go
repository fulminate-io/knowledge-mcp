// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/servicebus/armservicebus/v2"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// sbQueueDeadLetterEdge emits a DEAD_LETTERS_TO edge if the queue has dead
// letter forwarding configured. When ForwardDeadLetteredMessagesTo is set, the
// edge points to the forwarding destination. Otherwise, if
// DeadLetteringOnMessageExpiration is enabled, the edge points to the implicit
// dead-letter sub-queue (<queueID>/$deadletterqueue).
func sbQueueDeadLetterEdge(q *armservicebus.SBQueue) []cloud.EdgeSpec {
	if q.ID == nil || q.Properties == nil {
		return nil
	}

	// Explicit forwarding destination for dead-lettered messages.
	if q.Properties.ForwardDeadLetteredMessagesTo != nil && *q.Properties.ForwardDeadLetteredMessagesTo != "" {
		target := resolveEntityName(*q.ID, *q.Properties.ForwardDeadLetteredMessagesTo)
		return []cloud.EdgeSpec{{
			SourceID:     *q.ID,
			TargetID:     target,
			Relationship: kgtypes.EdgeDeadLettersTo,
		}}
	}

	// Implicit dead-letter sub-queue when expiration-based DLQ is enabled.
	if q.Properties.DeadLetteringOnMessageExpiration != nil && *q.Properties.DeadLetteringOnMessageExpiration {
		return []cloud.EdgeSpec{{
			SourceID:     *q.ID,
			TargetID:     *q.ID + "/$deadletterqueue",
			Relationship: kgtypes.EdgeDeadLettersTo,
		}}
	}

	return nil
}

// sbSubscriptionDeadLetterEdge emits a DEAD_LETTERS_TO edge if a topic
// subscription has dead letter forwarding configured.
func sbSubscriptionDeadLetterEdge(sub *armservicebus.SBSubscription) []cloud.EdgeSpec {
	if sub.ID == nil || sub.Properties == nil {
		return nil
	}

	if sub.Properties.ForwardDeadLetteredMessagesTo != nil && *sub.Properties.ForwardDeadLetteredMessagesTo != "" {
		target := resolveEntityName(*sub.ID, *sub.Properties.ForwardDeadLetteredMessagesTo)
		return []cloud.EdgeSpec{{
			SourceID:     *sub.ID,
			TargetID:     target,
			Relationship: kgtypes.EdgeDeadLettersTo,
		}}
	}

	if sub.Properties.DeadLetteringOnMessageExpiration != nil && *sub.Properties.DeadLetteringOnMessageExpiration {
		return []cloud.EdgeSpec{{
			SourceID:     *sub.ID,
			TargetID:     *sub.ID + "/$deadletterqueue",
			Relationship: kgtypes.EdgeDeadLettersTo,
		}}
	}

	return nil
}

// sbNamespaceEncryptionEdges emits ENCRYPTS_WITH edges for each Key Vault key
// configured on the namespace. Service Bus Premium namespaces support
// customer-managed keys (CMK) via the Encryption.KeyVaultProperties array.
func sbNamespaceEncryptionEdges(ns *armservicebus.SBNamespace) []cloud.EdgeSpec {
	if ns.ID == nil || ns.Properties == nil || ns.Properties.Encryption == nil {
		return nil
	}

	var edges []cloud.EdgeSpec
	for _, kvp := range ns.Properties.Encryption.KeyVaultProperties {
		if kvp == nil || kvp.KeyVaultURI == nil || kvp.KeyName == nil {
			continue
		}
		kvURI := *kvp.KeyVaultURI
		keyName := *kvp.KeyName
		if kvURI == "" || keyName == "" {
			continue
		}
		targetURI := strings.TrimRight(kvURI, "/") + "/keys/" + keyName
		if kvp.KeyVersion != nil && *kvp.KeyVersion != "" {
			targetURI += "/" + *kvp.KeyVersion
		}
		edges = append(edges, cloud.EdgeSpec{
			SourceID:     *ns.ID,
			TargetID:     targetURI,
			Relationship: kgtypes.EdgeEncryptsWith,
		})
	}
	return edges
}

// resolveEntityName converts a bare entity name (e.g. "my-dlq-queue") to a
// fully qualified ARM resource ID by replacing the last path segment of the
// source resource ID. If the name already looks like a path, it is returned
// as-is.
func resolveEntityName(sourceID, name string) string {
	if strings.HasPrefix(name, "/") {
		return name // already a full resource ID
	}
	// Replace the last segment: .../queues/original → .../queues/name
	idx := strings.LastIndex(sourceID, "/")
	if idx < 0 {
		return name
	}
	return sourceID[:idx+1] + name
}
