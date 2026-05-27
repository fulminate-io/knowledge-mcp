// SPDX-License-Identifier: Apache-2.0

package gcp

import "github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"

func init() {
	cloud.Register("gcp:compute:forwardingRule", summarizeForwardingRule)
	cloud.Register("gcp:compute:targetHttpProxy", summarizeTargetHTTPProxy)
	cloud.Register("gcp:compute:targetHttpsProxy", summarizeTargetHTTPSProxy)
	cloud.Register("gcp:compute:urlMap", summarizeURLMap)
	cloud.Register("gcp:compute:backendService", summarizeBackendService)
}

func summarizeForwardingRule(spec cloud.ResourceSpec) string {
	return gcpGenericSummary("forwarding rule", spec)
}

func summarizeTargetHTTPProxy(spec cloud.ResourceSpec) string {
	return gcpGenericSummary("target HTTP proxy", spec)
}

func summarizeTargetHTTPSProxy(spec cloud.ResourceSpec) string {
	return gcpGenericSummary("target HTTPS proxy", spec)
}

func summarizeURLMap(spec cloud.ResourceSpec) string {
	return gcpGenericSummary("URL map", spec)
}

func summarizeBackendService(spec cloud.ResourceSpec) string {
	return gcpGenericSummary("backend service", spec)
}
