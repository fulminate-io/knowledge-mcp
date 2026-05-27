// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func init() {
	cloud.Register("ConfigMap", summarizeConfigMap)
}

func summarizeConfigMap(spec cloud.ResourceSpec) string {
	parts := []string{"ConfigMap", spec.Name}
	if c := spec.Metadata["data_key_count"]; c != "" {
		parts = append(parts, fmt.Sprintf("keys=%s", c))
	}
	if ns := spec.Metadata["namespace"]; ns != "" {
		parts = append(parts, fmt.Sprintf("in %s", ns))
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}
