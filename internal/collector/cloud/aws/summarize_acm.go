// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func init() {
	cloud.Register("acm-certificate", summarizeACMCertificate)
}

func summarizeACMCertificate(spec cloud.ResourceSpec) string {
	parts := []string{"ACM cert", spec.Name}
	if dn := spec.Metadata["domain_name"]; dn != "" && dn != spec.Name {
		parts = append(parts, fmt.Sprintf("(%s)", dn))
	}
	if s := spec.Metadata["status"]; s != "" {
		parts = append(parts, fmt.Sprintf("status=%s", s))
	}
	if t := spec.Metadata["type"]; t != "" {
		parts = append(parts, fmt.Sprintf("type=%s", t))
	}
	if exp := spec.Metadata["not_after"]; exp != "" {
		parts = append(parts, fmt.Sprintf("expires=%s", exp))
	}
	if spec.Region != "" {
		parts = append(parts, fmt.Sprintf("in %s", spec.Region))
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}
