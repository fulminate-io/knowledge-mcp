// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func init() {
	cloud.Register("gcp:compute:disk", summarizeComputeDisk)
}

func summarizeComputeDisk(spec cloud.ResourceSpec) string {
	parts := []string{"GCE disk", spec.Name}
	if t := spec.Metadata["type"]; t != "" {
		parts = append(parts, fmt.Sprintf("type=%s", t))
	}
	if sz := spec.Metadata["sizeGb"]; sz != "" {
		parts = append(parts, fmt.Sprintf("size=%sGB", sz))
	}
	if s := spec.Metadata["status"]; s != "" {
		parts = append(parts, fmt.Sprintf("status=%s", s))
	}
	if z := spec.Metadata["zone"]; z != "" {
		parts = append(parts, fmt.Sprintf("in %s", z))
	} else if r := spec.Metadata["region"]; r != "" {
		parts = append(parts, fmt.Sprintf("in %s", r))
	} else if spec.Region != "" {
		parts = append(parts, fmt.Sprintf("in %s", spec.Region))
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}
