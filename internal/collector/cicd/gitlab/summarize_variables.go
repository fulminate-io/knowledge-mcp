// SPDX-License-Identifier: Apache-2.0

package gitlab

import (
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cicd"
)

func init() {
	cicd.Register("gitlab", "variable", summarizeGitLabVariable)
}

func summarizeGitLabVariable(spec cicd.ResourceSpec) string {
	parts := []string{"GitLab variable", spec.Name}
	if s := spec.Metadata["scope"]; s != "" {
		parts = append(parts, fmt.Sprintf("scope=%s", s))
	}
	if p := spec.Metadata["protected"]; p == "true" {
		parts = append(parts, "protected")
	}
	if m := spec.Metadata["masked"]; m == "true" {
		parts = append(parts, "masked")
	}
	if proj := spec.Metadata["project"]; proj != "" {
		parts = append(parts, fmt.Sprintf("(%s)", proj))
	} else if g := spec.Metadata["group"]; g != "" {
		parts = append(parts, fmt.Sprintf("(%s)", g))
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}
