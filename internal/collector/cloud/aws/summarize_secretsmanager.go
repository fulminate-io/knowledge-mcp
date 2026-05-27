// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

func init() {
	cloud.Register("secretsmanager-secret", summarizeSecretsManagerSecret)
}

func summarizeSecretsManagerSecret(spec cloud.ResourceSpec) string {
	parts := []string{"Secrets Manager secret", spec.Name}
	if d := spec.Metadata["description"]; d != "" {
		parts = append(parts, fmt.Sprintf("(%s)", d))
	}
	if k := spec.Metadata["kms_key_id"]; k != "" {
		parts = append(parts, "kms-encrypted")
	}
	if spec.Region != "" {
		parts = append(parts, fmt.Sprintf("in %s", spec.Region))
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}
