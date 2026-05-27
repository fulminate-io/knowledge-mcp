// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestGCPGenericSummary_ZonePreferred(t *testing.T) {
	got := gcpGenericSummary("Thing", cloud.ResourceSpec{
		Name:     "x",
		Region:   "us-central1",
		Metadata: map[string]string{"zone": "us-central1-a", "region": "us-central1"},
	})
	assert.Equal(t, "Thing x in us-central1-a", got)
}

func TestGCPGenericSummary_RegionFromMetadata(t *testing.T) {
	got := gcpGenericSummary("Thing", cloud.ResourceSpec{
		Name:     "x",
		Metadata: map[string]string{"region": "us-central1"},
	})
	assert.Equal(t, "Thing x in us-central1", got)
}

func TestGCPGenericSummary_RegionFromSpec(t *testing.T) {
	got := gcpGenericSummary("Thing", cloud.ResourceSpec{
		Name:   "x",
		Region: "us-central1",
	})
	assert.Equal(t, "Thing x in us-central1", got)
}

func TestGCPGenericSummary_NoLocation(t *testing.T) {
	got := gcpGenericSummary("Thing", cloud.ResourceSpec{Name: "x"})
	assert.Equal(t, "Thing x", got)
}
