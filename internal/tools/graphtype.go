// SPDX-License-Identifier: Apache-2.0

// graphtype.go — client-side intercept for the `custom_collector` MCP tool,
// which is READ-ONLY: one operation, `list`, over the graph-resident behavior
// records the server holds, read through a wire-loopback client
// (deps.GraphTypeCRUD()).
//
// THE WRITE OPERATIONS WERE REMOVED WITH THE REGISTRATION CONTRACT. A custom
// collector is registered by an entry in a `collectors.json` config file, so a
// tool that also wrote a record would be a SECOND writer for one registration —
// the two-surfaces cost the config-file design exists to avoid. `knowledge
// collector add | list | get | remove` is the write surface.
//
// LIST SURVIVES BECAUSE IT ANSWERS A QUESTION NOTHING ELSE DOES: it reports what
// the SERVER holds, which is what the routing gate and the whole summarize /
// embed / sync pipeline actually read. That makes it the diagnostic for both
// directions of disagreement — an entry present in a file whose family is not
// yet searchable, and a legacy catalog family with no entry at all.
//
// The custom_collector tool schema lives client-side at
// cmd/knowledge/internal/tools/graphtype_schema.go (GraphTypeToolDef);
// cmd/knowledge.loadSchemas appends it to the merged tool set so tools/list
// advertises the full op surface.

package tools

import (
	"context"
	"encoding/json"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// GraphTypeCRUDAPI is the narrow surface this package calls on the client-side
// wire-loopback CRUD client. *graphtypecrud.Client satisfies this interface
// structurally; tests inject a fake. It is the narrow-CRUD-surface convention
// applied to the gen *knowledgev1.GraphTypeDef record type.
//
// UPDATE IS THE ONE WRITER, and it writes the BEHAVIOR record alone: the config
// loader forwards an entry's behavior to the server on every resolve so the
// family is routable, searchable and syncable. Update and Create compile to the
// identical mutate(upsert) wire call.
type GraphTypeCRUDAPI interface {
	List(ctx context.Context) ([]*knowledgev1.GraphTypeDef, error)
	ByName(ctx context.Context, name string) (*knowledgev1.GraphTypeDef, bool, error)
	Update(ctx context.Context, d *knowledgev1.GraphTypeDef) error
}

// InterceptGraphType is the entry point invoked by the intercept chain. Returns
// (true, result) when the call was handled; (false, zero) when the call is not a
// custom_collector call and should fall through — the same name-filtering and
// fall-through convention every intercept in the chain follows.
func InterceptGraphType(ctx context.Context, deps ClientDeps, params kgtools.CallToolParams) (bool, kgtools.ToolResult) {
	if params.Name != "custom_collector" {
		return false, kgtools.ToolResult{}
	}
	if err := rejectUndeclaredParams("custom_collector", "", GraphTypeToolDef().InputSchema.Properties, params.Arguments); err != nil {
		return true, errorResult(err.Error())
	}
	var a graphTypeArgs
	if err := json.Unmarshal(params.Arguments, &a); err != nil {
		return true, errorResult("custom_collector: invalid arguments: " + decodeArgsError(params.Arguments, err))
	}

	switch a.Operation {
	case "list":
		return true, handleGraphTypeList(ctx, deps, a)
	default:
		return true, unknownOperationResult("custom_collector", a.Operation, []string{"list"})
	}
}
