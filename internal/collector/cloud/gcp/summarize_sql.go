// SPDX-License-Identifier: Apache-2.0

package gcp

import "github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"

func init() {
	cloud.Register("gcp:sql:instance", summarizeCloudSQLInstance)
}

func summarizeCloudSQLInstance(spec cloud.ResourceSpec) string {
	return gcpGenericSummary("Cloud SQL instance", spec)
}
