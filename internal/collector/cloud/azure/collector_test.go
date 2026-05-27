// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestAzureCollector_Name(t *testing.T) {
	c := &AzureCollector{}
	assert.Equal(t, "azure", c.Name())
}

func TestBuildSubCollectors_Count(t *testing.T) {
	// All 36 subcollectors should be wired when credentials are provided.
	// 14 original + functions + appservice + servicebus + appgateway + frontdoor + eventhub + firewall
	// + apim + logicapps + eventgrid + privateendpoints + privatedns + metricalerts + vnetpeering + disks
	// + redis + natgateways + certificates + search + flowlogs + synapse + aad-groups = 36.
	// Note: diagnostic settings are collected inline by the existing monitoring collector.
	subs := buildSubCollectors(nil, "test-sub")
	assert.Len(t, subs, 36)
}

func TestBuildSubCollectors_Names(t *testing.T) {
	// Verify each subcollector has a unique, non-empty name.
	subs := buildSubCollectors(nil, "test-sub")

	seen := make(map[string]bool)
	for _, sub := range subs {
		name := sub.Name()
		assert.NotEmpty(t, name)
		assert.False(t, seen[name], "duplicate subcollector name: %s", name)
		seen[name] = true
	}
}
