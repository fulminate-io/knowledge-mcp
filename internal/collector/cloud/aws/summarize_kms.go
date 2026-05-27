// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func init() {
	cloud.Register("kms-key", summarizeKMSKey)
}

func summarizeKMSKey(spec cloud.ResourceSpec) string {
	parts := []string{"KMS key", spec.Name}
	if k := spec.Metadata["KeyManager"]; k != "" {
		parts = append(parts, fmt.Sprintf("manager=%s", k))
	}
	if s := spec.Metadata["key_state"]; s != "" {
		parts = append(parts, fmt.Sprintf("state=%s", s))
	}
	if u := spec.Metadata["key_usage"]; u != "" {
		parts = append(parts, fmt.Sprintf("usage=%s", u))
	}
	if spec.Region != "" {
		parts = append(parts, fmt.Sprintf("in %s", spec.Region))
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}
