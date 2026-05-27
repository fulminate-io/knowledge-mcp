// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func init() {
	cloud.Register("lambda-function", summarizeLambdaFunction)
}

func summarizeLambdaFunction(spec cloud.ResourceSpec) string {
	parts := []string{"Lambda function", spec.Name}
	if r := spec.Metadata["runtime"]; r != "" {
		parts = append(parts, fmt.Sprintf("runtime=%s", r))
	}
	if m := spec.Metadata["memory_size"]; m != "" {
		parts = append(parts, fmt.Sprintf("memory=%sMB", m))
	}
	if a := spec.Metadata["function_url_auth_type"]; a != "" {
		parts = append(parts, fmt.Sprintf("url-auth=%s", a))
	}
	if spec.Region != "" {
		parts = append(parts, fmt.Sprintf("in %s", spec.Region))
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}
