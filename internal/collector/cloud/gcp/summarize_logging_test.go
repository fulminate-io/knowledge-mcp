// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeLoggingSink(t *testing.T) {
	assert.Equal(t, "Cloud Logging sink ls", summarizeLoggingSink(cloud.ResourceSpec{Name: "ls"}))
}
