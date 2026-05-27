// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func init() {
	cloud.Register("sns-topic", summarizeSNSTopic)
}

func summarizeSNSTopic(spec cloud.ResourceSpec) string {
	parts := []string{"SNS topic", spec.Name}
	if d := spec.Metadata["display_name"]; d != "" {
		parts = append(parts, fmt.Sprintf("(%s)", d))
	}
	if f := spec.Metadata["fifo_topic"]; f == "true" {
		parts = append(parts, "FIFO")
	}
	if c := spec.Metadata["subscriptions_confirmed"]; c != "" && c != "0" {
		parts = append(parts, fmt.Sprintf("subs=%s", c))
	}
	if spec.Region != "" {
		parts = append(parts, fmt.Sprintf("in %s", spec.Region))
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}
