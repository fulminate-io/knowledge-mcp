// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizePrivateDNSZone(t *testing.T) {
	assert.Equal(t, "private DNS zone z", summarizePrivateDNSZone(cloud.ResourceSpec{Name: "z"}))
}
