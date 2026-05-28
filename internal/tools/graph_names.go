// SPDX-License-Identifier: Apache-2.0

// Package tools — graph_names.go holds the client-side graph-catalog
// enumeration helper the cross-graph locator, repo resolver, and cloud/cicd
// overview share. It reduces the bespoke pipeline_list_graphs RPC to the generic
// Execute seam (RETURN_MODE_GRAPH_NAMES), mirroring linker.fetchGraphNames but
// living in package tools and returning the full []*knowledgev1.GraphInfo (callers
// project Name themselves; the D2 list_logs caller needs the full
// FilePath/FileSize/Nodes/Edges/Loaded fields).

package tools

import (
	"context"
	"encoding/json"
	"fmt"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
)

// fetchGraphNamesOfType enumerates every loaded graph of graphType via the
// generic Execute seam: a query(mode:modules) compiled to RETURN_MODE_GRAPH_NAMES
// whose graph_names_json carrier decodes to []*knowledgev1.GraphInfo. It replaces
// the bespoke pipeline_list_graphs RPC (the count-free ListGraphsLite read the
// server runs is the same one the modules query lowers to). One Execute per call,
// no fan-out.
//
// Returns the FULL []*knowledgev1.GraphInfo (NOT []string) so callers project Name
// themselves and the list_logs caller can reuse the FilePath/FileSize/Nodes/
// Edges/Loaded fields buildLogGraphSummary needs. A missing-seam / decode failure
// surfaces as an error so the OBJECT-shape mismatch is loud (mirroring the linker
// helper's non-masking contract).
func fetchGraphNamesOfType(ctx context.Context, gc GraphCaller, graphType string) ([]*knowledgev1.GraphInfo, error) {
	if gc == nil {
		return nil, fmt.Errorf("fetchGraphNamesOfType: graph caller unavailable")
	}
	args, err := json.Marshal(map[string]any{
		"graph": graphType,
		"mode":  "modules",
	})
	if err != nil {
		return nil, fmt.Errorf("fetchGraphNamesOfType(%s): marshal: %w", graphType, err)
	}
	resp, err := executeQuery(ctx, gc, args)
	if err != nil {
		return nil, fmt.Errorf("fetchGraphNamesOfType(%s): %w", graphType, err)
	}
	infos, err := engine.DecodeGraphNames(resp)
	if err != nil {
		return nil, fmt.Errorf("fetchGraphNamesOfType(%s): decode: %w", graphType, err)
	}
	return infos, nil
}
