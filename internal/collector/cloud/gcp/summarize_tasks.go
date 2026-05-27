// SPDX-License-Identifier: Apache-2.0

package gcp

import "github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"

func init() {
	cloud.Register("gcp:cloudtasks:queue", summarizeCloudTasksQueue)
}

func summarizeCloudTasksQueue(spec cloud.ResourceSpec) string {
	return gcpGenericSummary("Cloud Tasks queue", spec)
}
