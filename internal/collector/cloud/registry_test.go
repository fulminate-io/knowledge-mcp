// SPDX-License-Identifier: Apache-2.0

package cloud_test

import (
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/collector"

	// Blank imports trigger init() → collector.Register() for each cloud provider.
	_ "github.com/fulminate-io/knowledge-mcp/internal/collector/cloud/aws"
	_ "github.com/fulminate-io/knowledge-mcp/internal/collector/cloud/azure"
	_ "github.com/fulminate-io/knowledge-mcp/internal/collector/cloud/gcp"
	_ "github.com/fulminate-io/knowledge-mcp/internal/collector/cloud/k8s"
)

func TestAllCloudCollectorsRegistered(t *testing.T) {
	expected := []string{"aws", "azure", "gcp", "k8s"}
	for _, name := range expected {
		c, err := collector.Lookup(name)
		if err != nil {
			t.Errorf("collector.Lookup(%q) returned error: %v", name, err)
			continue
		}
		if c.Name() != name {
			t.Errorf("collector.Lookup(%q).Name() = %q, want %q", name, c.Name(), name)
		}
	}
}
