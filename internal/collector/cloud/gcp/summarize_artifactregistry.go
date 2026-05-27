// SPDX-License-Identifier: Apache-2.0

package gcp

import "github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"

func init() {
	cloud.Register("gcp:artifactregistry:repository", summarizeARRepository)
	cloud.Register("gcp:ar:remote", summarizeARRemote)
}

func summarizeARRepository(spec cloud.ResourceSpec) string {
	return gcpGenericSummary("Artifact Registry repository", spec)
}

func summarizeARRemote(spec cloud.ResourceSpec) string {
	return gcpGenericSummary("Artifact Registry remote", spec)
}
