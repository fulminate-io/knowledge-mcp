// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func init() {
	cloud.Register("Pod", summarizePod)
}

func summarizePod(spec cloud.ResourceSpec) string {
	parts := []string{"Pod", spec.Name}
	if p := spec.Metadata["phase"]; p != "" {
		parts = append(parts, fmt.Sprintf("phase=%s", p))
	}
	if n := spec.Metadata["node_name"]; n != "" {
		parts = append(parts, fmt.Sprintf("node=%s", n))
	}
	if r := spec.Metadata["restarts"]; r != "" && r != "0" {
		parts = append(parts, fmt.Sprintf("restarts=%s", r))
	}
	if ns := spec.Metadata["namespace"]; ns != "" {
		parts = append(parts, fmt.Sprintf("in %s", ns))
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}
