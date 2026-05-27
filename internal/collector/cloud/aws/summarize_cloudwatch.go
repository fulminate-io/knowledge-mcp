// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func init() {
	cloud.Register("cloudwatch-loggroup", summarizeCloudWatchLogGroup)
}

func summarizeCloudWatchLogGroup(spec cloud.ResourceSpec) string {
	parts := []string{"CloudWatch log group", spec.Name}
	if r := spec.Metadata["retention_days"]; r != "" {
		parts = append(parts, fmt.Sprintf("retention=%s days", r))
	}
	if k := spec.Metadata["kms_key_id"]; k != "" {
		parts = append(parts, "kms-encrypted")
	}
	if spec.Region != "" {
		parts = append(parts, fmt.Sprintf("in %s", spec.Region))
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}
