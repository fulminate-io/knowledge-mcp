// SPDX-License-Identifier: Apache-2.0

package k8s

import "github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"

func init() {
	cloud.Register("Job", summarizeJob)
	cloud.Register("CronJob", summarizeCronJob)
}

func summarizeJob(spec cloud.ResourceSpec) string {
	return k8sNamespacedSummary("Job", spec)
}

func summarizeCronJob(spec cloud.ResourceSpec) string {
	return k8sNamespacedSummary("CronJob", spec)
}
