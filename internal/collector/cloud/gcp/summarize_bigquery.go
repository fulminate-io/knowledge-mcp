// SPDX-License-Identifier: Apache-2.0

package gcp

import "github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"

func init() {
	cloud.Register("gcp:bigquery:dataset", summarizeBQDataset)
	cloud.Register("gcp:bigquery:table", summarizeBQTable)
}

func summarizeBQDataset(spec cloud.ResourceSpec) string {
	return gcpGenericSummary("BigQuery dataset", spec)
}

func summarizeBQTable(spec cloud.ResourceSpec) string {
	return gcpGenericSummary("BigQuery table", spec)
}
