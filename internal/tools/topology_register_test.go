// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/topology/corpusscan"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

// TestTopologyRegister_CorpusScanRegistered is the NAMED CATCHER for an omitted
// blank import in topology_register.go.
//
// A missing blank import is not compile-caught: the package simply never
// registers, the build stays green, and every corpus_scan call returns "unknown
// analyzer" at runtime. This test is the only thing that fails in that state.
func TestTopologyRegister_CorpusScanRegistered(t *testing.T) {
	a, ok := foundation.Get(corpusscan.AnalyzerName)
	if !ok {
		t.Fatalf("analyzer %q is not registered — topology_register.go is missing its blank import", corpusscan.AnalyzerName)
	}
	if a.Name() != corpusscan.AnalyzerName {
		t.Errorf("registered under %q but reports Name()=%q", corpusscan.AnalyzerName, a.Name())
	}
	// CONTROL: the registry lookup itself discriminates, so a green above cannot
	// come from a Get that answers ok for anything.
	if _, ok := foundation.Get("corpus_scan_does_not_exist"); ok {
		t.Error("control: foundation.Get answered ok for an unregistered name, so the assertion above proves nothing")
	}
}
