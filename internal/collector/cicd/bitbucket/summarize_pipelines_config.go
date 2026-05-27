// SPDX-License-Identifier: Apache-2.0

package bitbucket

import (
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cicd"
)

func init() {
	cicd.Register("bitbucket", "pipeline", summarizeBBPipeline)
}

func summarizeBBPipeline(spec cicd.ResourceSpec) string {
	parts := []string{"Bitbucket pipeline", spec.Name}
	if t := spec.Metadata["trigger_key"]; t != "" {
		parts = append(parts, fmt.Sprintf("trigger=%s", t))
	}
	if ws := spec.Metadata["workspace"]; ws != "" {
		if r := spec.Metadata["repo"]; r != "" {
			parts = append(parts, fmt.Sprintf("(%s/%s)", ws, r))
		} else {
			parts = append(parts, fmt.Sprintf("(%s)", ws))
		}
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}
