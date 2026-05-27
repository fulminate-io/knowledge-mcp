// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func init() {
	cloud.Register("Secret", summarizeSecret)
}

func summarizeSecret(spec cloud.ResourceSpec) string {
	parts := []string{"Secret", spec.Name}
	if t := spec.Metadata["type"]; t != "" {
		parts = append(parts, fmt.Sprintf("type=%s", t))
	}
	if ns := spec.Metadata["namespace"]; ns != "" {
		parts = append(parts, fmt.Sprintf("in %s", ns))
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}
