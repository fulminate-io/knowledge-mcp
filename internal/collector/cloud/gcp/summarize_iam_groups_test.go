// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeCloudIdentityGroup(t *testing.T) {
	assert.Equal(t, "Cloud Identity group g", summarizeCloudIdentityGroup(cloud.ResourceSpec{Name: "g"}))
}
