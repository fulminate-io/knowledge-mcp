// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeVPCAccessConnector(t *testing.T) {
	assert.Equal(t, "Serverless VPC connector c", summarizeVPCAccessConnector(cloud.ResourceSpec{Name: "c"}))
}
