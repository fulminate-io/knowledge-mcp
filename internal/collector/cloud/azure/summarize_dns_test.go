// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeDNSZone(t *testing.T) {
	assert.Equal(t, "Azure DNS zone z", summarizeDNSZone(cloud.ResourceSpec{Name: "z"}))
}

func TestSummarizeDNSRecordSet(t *testing.T) {
	assert.Equal(t, "Azure DNS record set r", summarizeDNSRecordSet(cloud.ResourceSpec{Name: "r"}))
}
