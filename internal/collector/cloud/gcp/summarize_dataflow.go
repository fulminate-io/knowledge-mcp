// SPDX-License-Identifier: Apache-2.0

package gcp

import "github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"

func init() {
	cloud.Register("gcp:dataflow:job", summarizeDataflowJob)
}

func summarizeDataflowJob(spec cloud.ResourceSpec) string {
	return gcpGenericSummary("Dataflow job", spec)
}
