// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func init() {
	cloud.Register("ec2-instance", summarizeEC2Instance)
}

func summarizeEC2Instance(spec cloud.ResourceSpec) string {
	parts := []string{"EC2 instance", spec.Name}
	if t := spec.Metadata["instance_type"]; t != "" {
		parts = append(parts, fmt.Sprintf("type=%s", t))
	}
	if s := spec.Metadata["state"]; s != "" {
		parts = append(parts, fmt.Sprintf("state=%s", s))
	}
	if v := spec.Metadata["vpc_id"]; v != "" {
		parts = append(parts, fmt.Sprintf("vpc=%s", v))
	}
	if az := spec.Metadata["availability_zone"]; az != "" {
		parts = append(parts, fmt.Sprintf("in %s", az))
	} else if spec.Region != "" {
		parts = append(parts, fmt.Sprintf("in %s", spec.Region))
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}
