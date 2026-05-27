// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func init() {
	cloud.Register("ecr-repository", summarizeECRRepository)
}

func summarizeECRRepository(spec cloud.ResourceSpec) string {
	parts := []string{"ECR repository", spec.Name}
	if im := spec.Metadata["image_tag_mutability"]; im != "" {
		parts = append(parts, fmt.Sprintf("mutability=%s", im))
	}
	if et := spec.Metadata["encryption_type"]; et != "" {
		parts = append(parts, fmt.Sprintf("encryption=%s", et))
	}
	if spec.Region != "" {
		parts = append(parts, fmt.Sprintf("in %s", spec.Region))
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}
