// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/eventhub/armeventhub"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// ehNamespaceEncryptionEdges emits ENCRYPTS_WITH edges for each Key Vault key
// configured on an Event Hub namespace via customer-managed encryption.
func ehNamespaceEncryptionEdges(ns *armeventhub.EHNamespace) []cloud.EdgeSpec {
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

// ehCaptureEdge emits a SINKS_TO edge if an event hub has capture enabled
// with a storage account destination. Per architectural decision, Event Hub
// capture uses SINKS_TO (archival/streaming) not DEAD_LETTERS_TO.
func ehCaptureEdge(eh *armeventhub.Eventhub) []cloud.EdgeSpec {
	if eh.ID == nil || eh.Properties == nil {
		return nil
	}
	cap := eh.Properties.CaptureDescription
	if cap == nil || cap.Enabled == nil || !*cap.Enabled {
		return nil
	}
	if cap.Destination == nil || cap.Destination.Properties == nil {
		return nil
	}
	storageID := cap.Destination.Properties.StorageAccountResourceID
	if storageID == nil || *storageID == "" {
		return nil
	}
	return []cloud.EdgeSpec{{
		SourceID:     *eh.ID,
		TargetID:     *storageID,
		Relationship: kgtypes.EdgeSinksTo,
	}}
}
