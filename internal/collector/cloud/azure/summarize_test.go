// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestAzureGenericSummary_LocationFromMetadata(t *testing.T) {
	got := azureGenericSummary("Thing", cloud.ResourceSpec{
		Name: "x", Region: "eastus",
		Metadata: map[string]string{"location": "westeurope"},
	})
	assert.Equal(t, "Thing x in westeurope", got)
}

func TestAzureGenericSummary_LocationFromSpec(t *testing.T) {
	got := azureGenericSummary("Thing", cloud.ResourceSpec{Name: "x", Region: "eastus"})
	assert.Equal(t, "Thing x in eastus", got)
}

func TestAzureGenericSummary_NoLocation(t *testing.T) {
	got := azureGenericSummary("Thing", cloud.ResourceSpec{Name: "x"})
	assert.Equal(t, "Thing x", got)
}
