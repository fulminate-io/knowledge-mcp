// SPDX-License-Identifier: Apache-2.0

package linker

import (
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// graphNamesToProto builds the typed GraphNames carrier from a list of graph
// names, so the server-simulating fakes in this package populate the carrier
// the real server now emits (FUL-276 migrated graph_names_json → repeated
// GraphInfo). The test fakes only ever set Name; the remaining GraphInfo fields
// stay zero, matching the modules-enumeration carrier the linker decode reads.
func graphNamesToProto(names []string) []*knowledgev1.GraphInfo {
	out := make([]*knowledgev1.GraphInfo, len(names))
	for i, n := range names {
		out[i] = &knowledgev1.GraphInfo{Name: n}
	}
	return out
}
