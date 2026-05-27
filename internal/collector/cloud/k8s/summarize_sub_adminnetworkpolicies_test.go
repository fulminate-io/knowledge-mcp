// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
)

// AdminNetworkPolicy doesn't register a literal key; the runtime fallback in
// the summarize registry handles dynamic kinds. This sentinel test asserts
// the package-level fallback behaves predictably for that case.
func TestAdminNetworkPolicyFallback(t *testing.T) {
	got := cloud.Summarize(cloud.ResourceSpec{
		ResourceType: "AdminNetworkPolicy:my-policy",
		Name:         "my-anp",
	})
	assert.Contains(t, got, "AdminNetworkPolicy:my-policy")
	assert.Contains(t, got, "my-anp")
}
