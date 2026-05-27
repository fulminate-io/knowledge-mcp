// SPDX-License-Identifier: Apache-2.0

package gitlab

import (
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cicd"
)

func init() {
	cicd.Register("gitlab", "oidc-issuer", summarizeGitLabOIDCIssuer)
}

func summarizeGitLabOIDCIssuer(spec cicd.ResourceSpec) string {
	parts := []string{"GitLab OIDC issuer", spec.Name}
	if iss := spec.Metadata["issuer"]; iss != "" {
		parts = append(parts, fmt.Sprintf("issuer=%s", iss))
	}
	if g := spec.Metadata["group"]; g != "" {
		parts = append(parts, fmt.Sprintf("(%s)", g))
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}
