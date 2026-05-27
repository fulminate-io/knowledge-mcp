// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func init() {
	cloud.Register("sqs-queue", summarizeSQSQueue)
}

func summarizeSQSQueue(spec cloud.ResourceSpec) string {
	parts := []string{"SQS queue", spec.Name}
	if f := spec.Metadata["fifo_queue"]; f == "true" {
		parts = append(parts, "FIFO")
	}
	if v := spec.Metadata["visibility_timeout"]; v != "" {
		parts = append(parts, fmt.Sprintf("vt=%ss", v))
	}
	if c := spec.Metadata["approximate_number_of_messages"]; c != "" && c != "0" {
		parts = append(parts, fmt.Sprintf("messages=%s", c))
	}
	if spec.Region != "" {
		parts = append(parts, fmt.Sprintf("in %s", spec.Region))
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}
