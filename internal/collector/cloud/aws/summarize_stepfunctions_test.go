// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func TestSummarizeStepFunctionsStateMachine(t *testing.T) {
	got := summarizeStepFunctionsStateMachine(cloud.ResourceSpec{
		Name: "wf-1", Region: "us-east-1",
		Metadata: map[string]string{"type": "STANDARD", "status": "ACTIVE", "logging_level": "ALL"},
	})
	assert.Contains(t, got, "Step Functions state machine wf-1")
	assert.Contains(t, got, "type=STANDARD")
	assert.Contains(t, got, "status=ACTIVE")
	assert.Contains(t, got, "log=ALL")
}

func TestSummarizeStepFunctionsStateMachine_EmptyMeta(t *testing.T) {
	assert.Equal(t, "Step Functions state machine x", summarizeStepFunctionsStateMachine(cloud.ResourceSpec{Name: "x"}))
}
