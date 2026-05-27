// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizePVC(t *testing.T) {
	assert.Equal(t, "PVC pvc", summarizePVC(cloud.ResourceSpec{Name: "pvc"}))
}
