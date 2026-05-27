// SPDX-License-Identifier: Apache-2.0

package gcp

import "github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"

func init() {
	cloud.Register("gcp:logging:sink", summarizeLoggingSink)
}

func summarizeLoggingSink(spec cloud.ResourceSpec) string {
	return gcpGenericSummary("Cloud Logging sink", spec)
}
