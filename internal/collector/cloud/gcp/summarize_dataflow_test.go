// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeDataflowJob(t *testing.T) {
	assert.Equal(t, "Dataflow job j", summarizeDataflowJob(cloud.ResourceSpec{Name: "j"}))
}
