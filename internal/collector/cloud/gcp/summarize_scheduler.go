// SPDX-License-Identifier: Apache-2.0

package gcp

import "github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"

func init() {
	cloud.Register("gcp:scheduler:job", summarizeSchedulerJob)
}

func summarizeSchedulerJob(spec cloud.ResourceSpec) string {
	return gcpGenericSummary("Cloud Scheduler job", spec)
}
