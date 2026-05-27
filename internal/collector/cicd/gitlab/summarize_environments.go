// SPDX-License-Identifier: Apache-2.0

package gitlab

import (
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cicd"
)

func init() {
	cicd.Register("gitlab", "environment", summarizeGitLabEnvironment)
	cicd.Register("gitlab", "protection-rule", summarizeGitLabProtectionRule)
}

func summarizeGitLabEnvironment(spec cicd.ResourceSpec) string {
	return glGenericSummary("GitLab environment", spec)
}

func summarizeGitLabProtectionRule(spec cicd.ResourceSpec) string {
	parts := []string{"GitLab protection rule", spec.Name}
	if c := spec.Metadata["required_approval_count"]; c != "" {
		parts = append(parts, fmt.Sprintf("approvals=%s", c))
	}
	if e := spec.Metadata["environment"]; e != "" {
		parts = append(parts, fmt.Sprintf("env=%s", e))
	}
	if p := spec.Metadata["project"]; p != "" {
		parts = append(parts, fmt.Sprintf("(%s)", p))
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}
