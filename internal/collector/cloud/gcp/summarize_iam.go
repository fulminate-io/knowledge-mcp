// SPDX-License-Identifier: Apache-2.0

package gcp

import "github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"

func init() {
	cloud.Register("gcp:iam:serviceAccount", summarizeServiceAccount)
	cloud.Register("gcp:resourcemanager:project", summarizeProject)
}

func summarizeServiceAccount(spec cloud.ResourceSpec) string {
	return gcpGenericSummary("GCP service account", spec)
}

func summarizeProject(spec cloud.ResourceSpec) string {
	return gcpGenericSummary("GCP project", spec)
}
