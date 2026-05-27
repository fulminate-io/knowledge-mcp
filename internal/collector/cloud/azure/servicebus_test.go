// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestServiceBusCollector_Name(t *testing.T) {
	c := &serviceBusCollector{}
	assert.Equal(t, "azure-servicebus", c.Name())
}

func TestParseResourceGroup(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			"standard resource ID",
			"/subscriptions/sub-1/resourceGroups/my-rg/providers/Microsoft.ServiceBus/namespaces/ns-1",
			"my-rg",
		},
		{
			"case-insensitive matching",
			"/subscriptions/sub-1/resourcegroups/My-RG/providers/Microsoft.ServiceBus/namespaces/ns-1",
			"My-RG",
		},
		{
			"nested resource",
			"/subscriptions/sub-1/resourceGroups/rg-2/providers/Microsoft.ServiceBus/namespaces/ns-1/queues/q-1",
			"rg-2",
		},
		{
			"no resource group",
			"/subscriptions/sub-1/providers/Microsoft.ServiceBus/namespaces/ns-1",
			"",
		},
		{
			"empty string",
			"",
			"",
		},
		{
			"just resourceGroups with no value after",
			"/subscriptions/sub-1/resourceGroups",
			"",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, parseResourceGroup(tt.input))
		})
	}
}
