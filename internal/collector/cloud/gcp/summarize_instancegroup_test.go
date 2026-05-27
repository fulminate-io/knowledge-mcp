// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeInstanceGroup(t *testing.T) {
	assert.Equal(t, "instance group ig", summarizeInstanceGroup(cloud.ResourceSpec{Name: "ig"}))
}
