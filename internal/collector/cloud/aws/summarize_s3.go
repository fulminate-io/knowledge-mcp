// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func init() {
	cloud.Register("s3-bucket", summarizeS3Bucket)
}

func summarizeS3Bucket(spec cloud.ResourceSpec) string {
	parts := []string{"S3 bucket", spec.Name}
	if c := spec.Metadata["creation_date"]; c != "" {
		parts = append(parts, fmt.Sprintf("created=%s", c))
	}
	if spec.Region != "" {
		parts = append(parts, fmt.Sprintf("in %s", spec.Region))
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}
