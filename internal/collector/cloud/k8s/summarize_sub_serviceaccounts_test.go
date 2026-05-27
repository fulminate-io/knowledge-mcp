// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeServiceAccount(t *testing.T) {
	assert.Equal(t, "ServiceAccount sa", summarizeServiceAccount(cloud.ResourceSpec{Name: "sa"}))
}
