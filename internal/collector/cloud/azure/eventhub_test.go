// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/eventhub/armeventhub"
	"github.com/stretchr/testify/assert"
)

func TestEventHubCollector_Name(t *testing.T) {
	c := &eventHubCollector{}
	assert.Equal(t, "azure-eventhubs", c.Name())
}

func TestEhNamespaceResourceSpec(t *testing.T) {
	skuN := armeventhub.SKUNameStandard
	skuT := armeventhub.SKUTierStandard
	ns := &armeventhub.EHNamespace{
		ID:       new("/subscriptions/sub-1/resourceGroups/rg/providers/Microsoft.EventHub/namespaces/eh-1"),
		Name:     new("eh-1"),
		Location: new("eastus"),
		SKU: &armeventhub.SKU{
			Name: &skuN,
			Tier: &skuT,
		},
	}
	spec := ehNamespaceResourceSpec(ns, []byte("{}"))
	assert.Equal(t, *ns.ID, spec.ID)
	assert.Equal(t, "eh-1", spec.Name)
	assert.Equal(t, "Microsoft.EventHub/namespaces", spec.ResourceType)
	assert.Equal(t, "eastus", spec.Region)
	assert.Equal(t, string(armeventhub.SKUNameStandard), spec.Metadata["skuName"])
	assert.Equal(t, string(armeventhub.SKUTierStandard), spec.Metadata["skuTier"])
}

func TestEhNamespaceResourceSpec_MinimalFields(t *testing.T) {
	ns := &armeventhub.EHNamespace{
		ID:   new("/subscriptions/sub-1/resourceGroups/rg/providers/Microsoft.EventHub/namespaces/eh-min"),
		Name: new("eh-min"),
	}
	spec := ehNamespaceResourceSpec(ns, []byte("{}"))
	assert.Equal(t, "eh-min", spec.Name)
	assert.Empty(t, spec.Region)
	assert.Empty(t, spec.Metadata["skuName"])
}
