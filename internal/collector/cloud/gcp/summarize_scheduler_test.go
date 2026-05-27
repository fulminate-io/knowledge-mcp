// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeSchedulerJob(t *testing.T) {
	assert.Equal(t, "Cloud Scheduler job j", summarizeSchedulerJob(cloud.ResourceSpec{Name: "j"}))
}
