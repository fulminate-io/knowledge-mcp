// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeFilestoreInstance(t *testing.T) {
	assert.Equal(t, "Filestore instance fi", summarizeFilestoreInstance(cloud.ResourceSpec{Name: "fi"}))
}
