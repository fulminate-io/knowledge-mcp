// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func init() {
	cloud.Register("cloudwatch-alarm", summarizeCloudWatchAlarm)
}

func summarizeCloudWatchAlarm(spec cloud.ResourceSpec) string {
	parts := []string{"CloudWatch alarm", spec.Name}
	if ns := spec.Metadata["namespace"]; ns != "" {
		if mn := spec.Metadata["metric_name"]; mn != "" {
			parts = append(parts, fmt.Sprintf("on %s/%s", ns, mn))
		} else {
			parts = append(parts, fmt.Sprintf("on %s", ns))
		}
	}
	if s := spec.Metadata["state"]; s != "" {
		parts = append(parts, fmt.Sprintf("state=%s", s))
	}
	if spec.Region != "" {
		parts = append(parts, fmt.Sprintf("in %s", spec.Region))
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}
