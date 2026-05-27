// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func init() {
	cloud.Register("eks-cluster", summarizeEKSCluster)
}

func summarizeEKSCluster(spec cloud.ResourceSpec) string {
	parts := []string{"EKS cluster", spec.Name}
	if v := spec.Metadata["version"]; v != "" {
		parts = append(parts, fmt.Sprintf("k8s=%s", v))
	}
	if s := spec.Metadata["status"]; s != "" {
		parts = append(parts, fmt.Sprintf("status=%s", s))
	}
	if spec.Region != "" {
		parts = append(parts, fmt.Sprintf("in %s", spec.Region))
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}
