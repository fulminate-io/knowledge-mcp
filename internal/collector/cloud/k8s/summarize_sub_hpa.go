// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func init() {
	cloud.Register("HorizontalPodAutoscaler", summarizeHPA)
}

func summarizeHPA(spec cloud.ResourceSpec) string {
	parts := []string{"HPA", spec.Name}
	mn := spec.Metadata["min_replicas"]
	mx := spec.Metadata["max_replicas"]
	if mn != "" || mx != "" {
		parts = append(parts, fmt.Sprintf("range=%s..%s", mn, mx))
	}
	if c := spec.Metadata["current_replicas"]; c != "" {
		parts = append(parts, fmt.Sprintf("current=%s", c))
	}
	if ns := spec.Metadata["namespace"]; ns != "" {
		parts = append(parts, fmt.Sprintf("in %s", ns))
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}
