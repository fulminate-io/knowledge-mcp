// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeJob(t *testing.T) {
	assert.Equal(t, "Job j", summarizeJob(cloud.ResourceSpec{Name: "j"}))
}

func TestSummarizeCronJob(t *testing.T) {
	assert.Equal(t, "CronJob cj", summarizeCronJob(cloud.ResourceSpec{Name: "cj"}))
}
