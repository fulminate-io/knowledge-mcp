// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func init() {
	cloud.Register("iam-group", summarizeIAMGroup)
	cloud.Register("iam-policy", summarizeIAMPolicy)
	cloud.Register("iam-role", summarizeIAMRole)
	cloud.Register("iam-user", summarizeIAMUser)
}

func summarizeIAMGroup(spec cloud.ResourceSpec) string {
	parts := []string{"IAM group", spec.Name}
	if p := spec.Metadata["path"]; p != "" && p != "/" {
		parts = append(parts, fmt.Sprintf("path=%s", p))
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}

func summarizeIAMPolicy(spec cloud.ResourceSpec) string {
	parts := []string{"IAM policy", spec.Name}
	if c := spec.Metadata["attachment_count"]; c != "" {
		parts = append(parts, fmt.Sprintf("attachments=%s", c))
	}
	if p := spec.Metadata["path"]; p != "" {
		parts = append(parts, fmt.Sprintf("path=%s", p))
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}

func summarizeIAMRole(spec cloud.ResourceSpec) string {
	parts := []string{"IAM role", spec.Name}
	if p := spec.Metadata["path"]; p != "" && p != "/" {
		parts = append(parts, fmt.Sprintf("path=%s", p))
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}

func summarizeIAMUser(spec cloud.ResourceSpec) string {
	parts := []string{"IAM user", spec.Name}
	if p := spec.Metadata["path"]; p != "" && p != "/" {
		parts = append(parts, fmt.Sprintf("path=%s", p))
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}
