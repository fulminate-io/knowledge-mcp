// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func init() {
	cloud.Register("apigw:restapi", summarizeAPIGWRestAPI)
	cloud.Register("apigw:domain", summarizeAPIGWDomain)
}

func summarizeAPIGWRestAPI(spec cloud.ResourceSpec) string {
	parts := []string{"API Gateway REST API", spec.Name}
	if et := spec.Metadata["endpoint_type"]; et != "" {
		parts = append(parts, fmt.Sprintf("endpoint=%s", et))
	}
	if spec.Metadata["disable_execute_api_endpoint"] == "true" {
		parts = append(parts, "execute-api-disabled")
	}
	if spec.Region != "" {
		parts = append(parts, fmt.Sprintf("in %s", spec.Region))
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}

func summarizeAPIGWDomain(spec cloud.ResourceSpec) string {
	parts := []string{"API Gateway domain", spec.Name}
	if et := spec.Metadata["endpoint_type"]; et != "" {
		parts = append(parts, fmt.Sprintf("endpoint=%s", et))
	}
	if s := spec.Metadata["status"]; s != "" {
		parts = append(parts, fmt.Sprintf("status=%s", s))
	}
	if spec.Region != "" {
		parts = append(parts, fmt.Sprintf("in %s", spec.Region))
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}
