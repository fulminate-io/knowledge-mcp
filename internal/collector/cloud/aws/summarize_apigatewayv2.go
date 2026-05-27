// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func init() {
	cloud.Register("apigw:httpapi", summarizeAPIGWHTTPAPI)
	cloud.Register("apigw:wsapi", summarizeAPIGWWSAPI)
}

func summarizeAPIGWHTTPAPI(spec cloud.ResourceSpec) string {
	parts := []string{"API Gateway HTTP API", spec.Name}
	if p := spec.Metadata["protocol_type"]; p != "" {
		parts = append(parts, fmt.Sprintf("protocol=%s", p))
	}
	if spec.Region != "" {
		parts = append(parts, fmt.Sprintf("in %s", spec.Region))
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}

func summarizeAPIGWWSAPI(spec cloud.ResourceSpec) string {
	parts := []string{"API Gateway WebSocket API", spec.Name}
	if spec.Region != "" {
		parts = append(parts, fmt.Sprintf("in %s", spec.Region))
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}
