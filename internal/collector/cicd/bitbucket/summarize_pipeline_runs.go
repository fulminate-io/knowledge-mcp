// SPDX-License-Identifier: Apache-2.0

package bitbucket

import (
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cicd"
)

func init() {
	cicd.Register("bitbucket", "pipeline_run", summarizeBBPipelineRun)
}

func summarizeBBPipelineRun(spec cicd.ResourceSpec) string {
	parts := []string{"Bitbucket pipeline run", spec.Name}
	if s := spec.Metadata["state"]; s != "" {
		parts = append(parts, fmt.Sprintf("state=%s", s))
	}
	if r := spec.Metadata["result"]; r != "" {
		parts = append(parts, fmt.Sprintf("result=%s", r))
	}
	if ws := spec.Metadata["workspace"]; ws != "" {
		parts = append(parts, fmt.Sprintf("(%s)", ws))
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}
