// SPDX-License-Identifier: Apache-2.0

package gcp

import "github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"

func init() {
	cloud.Register("gcp:storage:bucket", summarizeStorageBucket)
}

func summarizeStorageBucket(spec cloud.ResourceSpec) string {
	return gcpGenericSummary("Cloud Storage bucket", spec)
}
