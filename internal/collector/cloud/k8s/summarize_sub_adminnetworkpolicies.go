// SPDX-License-Identifier: Apache-2.0

package k8s

// AdminNetworkPolicy resource type comes from a dynamic field in the
// subcollector (target.resourceType — sub_adminnetworkpolicies.go:164), not
// a literal. The audit allowlist absorbs the unregistered keys; the runtime
// fallback formats the Summary deterministically. No init/Register here.
