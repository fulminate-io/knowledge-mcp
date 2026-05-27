// SPDX-License-Identifier: Apache-2.0

package gcp

import "github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"

func init() {
	cloud.Register("gcp:secretmanager:secret", summarizeSecretManagerSecret)
}

func summarizeSecretManagerSecret(spec cloud.ResourceSpec) string {
	return gcpGenericSummary("Secret Manager secret", spec)
}
