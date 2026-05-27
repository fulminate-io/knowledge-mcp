// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func init() {
	cloud.Register("redshift-cluster", summarizeRedshiftCluster)
}

func summarizeRedshiftCluster(spec cloud.ResourceSpec) string {
	parts := []string{"Redshift cluster", spec.Name}
	if t := spec.Metadata["node_type"]; t != "" {
		parts = append(parts, fmt.Sprintf("node=%s", t))
	}
	if n := spec.Metadata["number_of_nodes"]; n != "" {
		parts = append(parts, fmt.Sprintf("nodes=%s", n))
	}
	if s := spec.Metadata["status"]; s != "" {
		parts = append(parts, fmt.Sprintf("status=%s", s))
	}
	if spec.Region != "" {
		parts = append(parts, fmt.Sprintf("in %s", spec.Region))
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}
