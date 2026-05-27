// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeARRepository(t *testing.T) {
	got := summarizeARRepository(cloud.ResourceSpec{Name: "repo", Region: "us-central1"})
	assert.Equal(t, "Artifact Registry repository repo in us-central1", got)
}

func TestSummarizeARRemote(t *testing.T) {
	got := summarizeARRemote(cloud.ResourceSpec{Name: "rmt"})
	assert.Equal(t, "Artifact Registry remote rmt", got)
}
