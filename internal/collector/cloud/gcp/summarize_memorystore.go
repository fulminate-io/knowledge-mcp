// SPDX-License-Identifier: Apache-2.0

package gcp

import "github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"

func init() {
	cloud.Register("gcp:redis:instance", summarizeRedisInstance)
}

func summarizeRedisInstance(spec cloud.ResourceSpec) string {
	return gcpGenericSummary("Memorystore Redis instance", spec)
}
