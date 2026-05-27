// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeSecretManagerSecret(t *testing.T) {
	assert.Equal(t, "Secret Manager secret s", summarizeSecretManagerSecret(cloud.ResourceSpec{Name: "s"}))
}
