// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func init() {
	cloud.Register("stepfunctions-statemachine", summarizeStepFunctionsStateMachine)
}

func summarizeStepFunctionsStateMachine(spec cloud.ResourceSpec) string {
	parts := []string{"Step Functions state machine", spec.Name}
	if t := spec.Metadata["type"]; t != "" {
		parts = append(parts, fmt.Sprintf("type=%s", t))
	}
	if s := spec.Metadata["status"]; s != "" {
		parts = append(parts, fmt.Sprintf("status=%s", s))
	}
	if l := spec.Metadata["logging_level"]; l != "" {
		parts = append(parts, fmt.Sprintf("log=%s", l))
	}
	if spec.Region != "" {
		parts = append(parts, fmt.Sprintf("in %s", spec.Region))
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}
