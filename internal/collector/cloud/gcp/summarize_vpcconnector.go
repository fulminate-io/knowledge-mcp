// SPDX-License-Identifier: Apache-2.0

package gcp

import "github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"

func init() {
	cloud.Register("gcp:vpcaccess:connector", summarizeVPCAccessConnector)
}

func summarizeVPCAccessConnector(spec cloud.ResourceSpec) string {
	return gcpGenericSummary("Serverless VPC connector", spec)
}
