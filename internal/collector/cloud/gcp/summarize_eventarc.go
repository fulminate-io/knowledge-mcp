// SPDX-License-Identifier: Apache-2.0

package gcp

import "github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"

func init() {
	cloud.Register("gcp:eventarc:trigger", summarizeEventarcTrigger)
}

func summarizeEventarcTrigger(spec cloud.ResourceSpec) string {
	return gcpGenericSummary("Eventarc trigger", spec)
}
