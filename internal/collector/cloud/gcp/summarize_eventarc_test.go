// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeEventarcTrigger(t *testing.T) {
	assert.Equal(t, "Eventarc trigger t", summarizeEventarcTrigger(cloud.ResourceSpec{Name: "t"}))
}
