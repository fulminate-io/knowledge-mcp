// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func init() {
	cloud.Register("cloudtrail-trail", summarizeCloudTrailTrail)
}

func summarizeCloudTrailTrail(spec cloud.ResourceSpec) string {
	parts := []string{"CloudTrail trail", spec.Name}
	if mr := spec.Metadata["multi_region"]; mr == "true" {
		parts = append(parts, "multi-region")
	}
	if hr := spec.Metadata["home_region"]; hr != "" {
		parts = append(parts, fmt.Sprintf("home=%s", hr))
	}
	if v := spec.Metadata["log_file_validation_enabled"]; v == "true" {
		parts = append(parts, "log-file-validation")
	}
	if spec.Region != "" {
		parts = append(parts, fmt.Sprintf("in %s", spec.Region))
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}
