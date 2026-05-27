// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func init() {
	cloud.Register("opensearch-domain", summarizeOpenSearchDomain)
}

func summarizeOpenSearchDomain(spec cloud.ResourceSpec) string {
	parts := []string{"OpenSearch domain", spec.Name}
	if v := spec.Metadata["engine_version"]; v != "" {
		parts = append(parts, fmt.Sprintf("version=%s", v))
	}
	if t := spec.Metadata["instance_type"]; t != "" {
		parts = append(parts, fmt.Sprintf("type=%s", t))
	}
	if c := spec.Metadata["instance_count"]; c != "" {
		parts = append(parts, fmt.Sprintf("nodes=%s", c))
	}
	if spec.Region != "" {
		parts = append(parts, fmt.Sprintf("in %s", spec.Region))
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}
