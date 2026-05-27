// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func init() {
	cloud.Register("Deployment", summarizeDeployment)
}

func summarizeDeployment(spec cloud.ResourceSpec) string {
	parts := []string{"Deployment", spec.Name}
	if r := spec.Metadata["replicas"]; r != "" {
		parts = append(parts, fmt.Sprintf("replicas=%s", r))
	}
	if s := spec.Metadata["strategy"]; s != "" {
		parts = append(parts, fmt.Sprintf("strategy=%s", s))
	}
	if ns := spec.Metadata["namespace"]; ns != "" {
		parts = append(parts, fmt.Sprintf("in %s", ns))
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}
