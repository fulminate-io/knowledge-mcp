// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func init() {
	cloud.Register("dynamodb-table", summarizeDynamoDBTable)
}

func summarizeDynamoDBTable(spec cloud.ResourceSpec) string {
	parts := []string{"DynamoDB table", spec.Name}
	if s := spec.Metadata["status"]; s != "" {
		parts = append(parts, fmt.Sprintf("status=%s", s))
	}
	if bm := spec.Metadata["billing_mode"]; bm != "" {
		parts = append(parts, fmt.Sprintf("billing=%s", bm))
	}
	if c := spec.Metadata["item_count"]; c != "" && c != "0" {
		parts = append(parts, fmt.Sprintf("items=%s", c))
	}
	if spec.Region != "" {
		parts = append(parts, fmt.Sprintf("in %s", spec.Region))
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}
