// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeBQDataset(t *testing.T) {
	assert.Equal(t, "BigQuery dataset ds", summarizeBQDataset(cloud.ResourceSpec{Name: "ds"}))
}

func TestSummarizeBQTable(t *testing.T) {
	assert.Equal(t, "BigQuery table tbl", summarizeBQTable(cloud.ResourceSpec{Name: "tbl"}))
}
