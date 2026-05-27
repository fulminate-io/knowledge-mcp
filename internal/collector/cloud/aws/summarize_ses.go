// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func init() {
	cloud.Register("ses-identity", summarizeSESIdentity)
}

func summarizeSESIdentity(spec cloud.ResourceSpec) string {
	parts := []string{"SES identity", spec.Name}
	if t := spec.Metadata["identity_type"]; t != "" {
		parts = append(parts, fmt.Sprintf("type=%s", t))
	}
	if v := spec.Metadata["verified_for_sending"]; v == "true" {
		parts = append(parts, "verified")
	}
	if spec.Region != "" {
		parts = append(parts, fmt.Sprintf("in %s", spec.Region))
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}
