// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func init() {
	cloud.Register("elbv2-loadbalancer", summarizeELBv2LoadBalancer)
	cloud.Register("elbv2-targetgroup", summarizeELBv2TargetGroup)
}

func summarizeELBv2LoadBalancer(spec cloud.ResourceSpec) string {
	parts := []string{"ELBv2 load balancer", spec.Name}
	if t := spec.Metadata["type"]; t != "" {
		parts = append(parts, fmt.Sprintf("type=%s", t))
	}
	if s := spec.Metadata["scheme"]; s != "" {
		parts = append(parts, fmt.Sprintf("scheme=%s", s))
	}
	if v := spec.Metadata["vpc_id"]; v != "" {
		parts = append(parts, fmt.Sprintf("vpc=%s", v))
	}
	if spec.Region != "" {
		parts = append(parts, fmt.Sprintf("in %s", spec.Region))
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}

func summarizeELBv2TargetGroup(spec cloud.ResourceSpec) string {
	parts := []string{"ELBv2 target group", spec.Name}
	if p := spec.Metadata["protocol"]; p != "" {
		parts = append(parts, fmt.Sprintf("proto=%s", p))
	}
	if pt := spec.Metadata["port"]; pt != "" {
		parts = append(parts, fmt.Sprintf("port=%s", pt))
	}
	if t := spec.Metadata["target_type"]; t != "" {
		parts = append(parts, fmt.Sprintf("target=%s", t))
	}
	if v := spec.Metadata["vpc_id"]; v != "" {
		parts = append(parts, fmt.Sprintf("vpc=%s", v))
	}
	if spec.Region != "" {
		parts = append(parts, fmt.Sprintf("in %s", spec.Region))
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}
