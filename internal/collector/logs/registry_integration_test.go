// SPDX-License-Identifier: Apache-2.0

package logs_test

import (
	"testing"

	logwire "github.com/fulminate-io/knowledge-mcp/internal/logwire"

	// Blank-import backends to trigger init() self-registration.
	_ "github.com/fulminate-io/knowledge-mcp/internal/collector/logs/cloudwatch"
	_ "github.com/fulminate-io/knowledge-mcp/internal/collector/logs/loki"
)

func TestNewReturnsProviderInstances(t *testing.T) {
	for _, name := range []string{"cloudwatch", "loki"} {
		p, err := logwire.New(name)
		if err != nil {
			t.Fatalf("New(%q): %v", name, err)
		}
		if p == nil {
			t.Fatalf("New(%q) returned nil provider", name)
		}
	}
}
