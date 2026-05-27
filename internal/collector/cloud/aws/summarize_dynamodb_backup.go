// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func init() {
	cloud.Register("dynamodb-pitr", summarizeDynamoDBPITR)
	cloud.Register("dynamodb-backup", summarizeDynamoDBBackup)
}

func summarizeDynamoDBPITR(spec cloud.ResourceSpec) string {
	parts := []string{"DynamoDB PITR", spec.Name}
	if s := spec.Metadata["status"]; s != "" {
		parts = append(parts, fmt.Sprintf("status=%s", s))
	}
	if spec.Region != "" {
		parts = append(parts, fmt.Sprintf("in %s", spec.Region))
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}

func summarizeDynamoDBBackup(spec cloud.ResourceSpec) string {
	parts := []string{"DynamoDB backup", spec.Name}
	if s := spec.Metadata["backup_status"]; s != "" {
		parts = append(parts, fmt.Sprintf("status=%s", s))
	}
	if t := spec.Metadata["backup_type"]; t != "" {
		parts = append(parts, fmt.Sprintf("type=%s", t))
	}
	if spec.Region != "" {
		parts = append(parts, fmt.Sprintf("in %s", spec.Region))
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}
