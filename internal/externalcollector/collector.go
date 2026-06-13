// SPDX-License-Identifier: Apache-2.0

package externalcollector

import (
	"context"
	"encoding/json"
	"fmt"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/collectorwire"
)

// RunExternal is the single composition seam the collect dispatch calls for a
// registered (non-builtin) graph type. It validates the incoming params against
// the registered schema, runs the external binary, guards the emitted graph
// type, applies the collect-id default for graph_name, and converts the envelope
// to the in-tree wire payload.
//
// defaultGraphName is the collect id; it supplies graph_name ONLY when the
// binary's envelope omitted it. An envelope that emits its own graph_name keeps
// it (the explicit value wins). When BOTH the envelope and defaultGraphName are
// empty the result is left empty, so the convert.go ToCollectResult guard still
// fails loud on the both-empty case.
//
// Steps:
//  1. ValidateParams against def's collector param_schema (collect-time gate).
//  2. Marshal params to the JSON the binary receives on stdin / its flag.
//  3. Run the binary and parse its JSON stdout into a *Result.
//  4. Guard: the emitted graph_type MUST equal def.Name. Without this a plugin
//     could write into a DIFFERENT (possibly another user's registered or a
//     built-in) graph type — the ticket requires the guard.
//  5. Default graph_name to defaultGraphName (the collect id) when the envelope
//     omitted it; an explicit envelope graph_name wins, both-empty stays empty.
//  6. Convert the envelope to *collectorwire.CollectResult for the UploadSink.
func RunExternal(
	ctx context.Context,
	def *knowledgev1.GraphTypeDef,
	params map[string]any,
	defaultGraphName string,
) (*collectorwire.CollectResult, error) {
	if def == nil {
		return nil, fmt.Errorf("externalcollector: nil GraphTypeDef")
	}
	col := def.GetCollector()
	if col == nil {
		return nil, fmt.Errorf("externalcollector: graph type %q has no collector spec", def.GetName())
	}

	if err := ValidateParams(col.GetParamSchema(), params); err != nil {
		return nil, err
	}

	paramsJSON, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("externalcollector: marshal params: %w", err)
	}

	result, err := Run(ctx, col, paramsJSON)
	if err != nil {
		return nil, err
	}

	if result.GraphType != def.GetName() {
		return nil, fmt.Errorf(
			"externalcollector: collector emitted graph_type %q, registered as %q — refusing to cross graph types",
			result.GraphType, def.GetName())
	}

	// Default graph_name to the collect id when the envelope omitted it. The
	// explicit envelope value wins; when both are empty the result stays empty so
	// ToCollectResult's empty-graph_name guard still fails loud.
	if result.GraphName == "" && defaultGraphName != "" {
		result.GraphName = defaultGraphName
	}

	return result.ToCollectResult()
}
