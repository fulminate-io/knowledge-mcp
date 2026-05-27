// SPDX-License-Identifier: Apache-2.0

package gcp

import "github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"

func init() {
	cloud.Register("gcp:file:instance", summarizeFilestoreInstance)
}

func summarizeFilestoreInstance(spec cloud.ResourceSpec) string {
	return gcpGenericSummary("Filestore instance", spec)
}
