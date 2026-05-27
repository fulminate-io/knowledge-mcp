// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeServiceAccount(t *testing.T) {
	assert.Equal(t, "GCP service account sa", summarizeServiceAccount(cloud.ResourceSpec{Name: "sa"}))
}

func TestSummarizeProject(t *testing.T) {
	assert.Equal(t, "GCP project p", summarizeProject(cloud.ResourceSpec{Name: "p"}))
}
