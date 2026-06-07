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
// type, and converts the envelope to the in-tree wire payload.
//
// Steps:
//  1. ValidateParams against def's collector param_schema (collect-time gate).
//  2. Marshal params to the JSON the binary receives on stdin / its flag.
//  3. Run the binary and parse its JSON stdout into a *Result.
//  4. Guard: the emitted graph_type MUST equal def.Name. Without this a plugin
//     could write into a DIFFERENT (possibly another user's registered or a
//     built-in) graph type — the ticket requires the guard.
//  5. Convert the envelope to *collectorwire.CollectResult for the UploadSink.
func RunExternal(
	ctx context.Context,
	def *knowledgev1.GraphTypeDef,
	params map[string]any,
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

	return result.ToCollectResult()
}
