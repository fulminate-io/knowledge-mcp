// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeDNSManagedZone(t *testing.T) {
	assert.Equal(t, "DNS managed zone z", summarizeDNSManagedZone(cloud.ResourceSpec{Name: "z"}))
}

func TestSummarizeDNSRecordSet(t *testing.T) {
	assert.Equal(t, "DNS record set r", summarizeDNSRecordSet(cloud.ResourceSpec{Name: "r"}))
}
