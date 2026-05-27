// SPDX-License-Identifier: Apache-2.0

package gcp

import "github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"

func init() {
	cloud.Register("gcp:cloudfunctions:function", summarizeCloudFunction)
}

func summarizeCloudFunction(spec cloud.ResourceSpec) string {
	return gcpGenericSummary("Cloud Function", spec)
}
