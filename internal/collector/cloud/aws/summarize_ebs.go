// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func init() {
	cloud.Register("ebs-volume", summarizeEBSVolume)
}

func summarizeEBSVolume(spec cloud.ResourceSpec) string {
	parts := []string{"EBS volume", spec.Name}
	if t := spec.Metadata["volume_type"]; t != "" {
		parts = append(parts, fmt.Sprintf("type=%s", t))
	}
	if sz := spec.Metadata["size_gib"]; sz != "" {
		parts = append(parts, fmt.Sprintf("size=%sGiB", sz))
	}
	if s := spec.Metadata["state"]; s != "" {
		parts = append(parts, fmt.Sprintf("state=%s", s))
	}
	if e := spec.Metadata["encrypted"]; e == "true" {
		parts = append(parts, "encrypted")
	}
	if az := spec.Metadata["availability_zone"]; az != "" {
		parts = append(parts, fmt.Sprintf("in %s", az))
	} else if spec.Region != "" {
		parts = append(parts, fmt.Sprintf("in %s", spec.Region))
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}
