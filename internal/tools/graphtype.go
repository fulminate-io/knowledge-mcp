// SPDX-License-Identifier: Apache-2.0

// graphtype.go — client-side intercept for the `custom_collector` MCP tool's
// register/update/delete/list operations. The record is a graph-resident
// config node owned by the server; every op is CRUD over it via a
// wire-loopback client (deps.GraphTypeCRUD()), mirroring the worker tool's
// list/create/update/delete handlers.
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

// GraphTypeCRUDAPI is the narrow surface the custom_collector handlers call on the
// client-side wire-loopback CRUD client. *graphtypecrud.Client satisfies this
// interface structurally; tests inject a fake. Mirrors WorkerCRUDAPI but over
// the gen *knowledgev1.GraphTypeDef record type.
type GraphTypeCRUDAPI interface {
	List(ctx context.Context) ([]*knowledgev1.GraphTypeDef, error)
	ByName(ctx context.Context, name string) (*knowledgev1.GraphTypeDef, bool, error)
	Create(ctx context.Context, d *knowledgev1.GraphTypeDef) error
	Update(ctx context.Context, d *knowledgev1.GraphTypeDef) error
	Delete(ctx context.Context, name string) error
}

// InterceptGraphType is the entry point invoked by the intercept chain. Returns
// (true, result) when the call was handled; (false, zero) when the call is not a
// custom_collector call and should fall through. Mirrors InterceptWorker.
func InterceptGraphType(ctx context.Context, deps ClientDeps, params kgtools.CallToolParams) (bool, kgtools.ToolResult) {
	if params.Name != "custom_collector" {
		return false, kgtools.ToolResult{}
	}
	if err := rejectUndeclaredParams("custom_collector", "", GraphTypeToolDef().InputSchema.Properties, params.Arguments); err != nil {
		return true, errorResult(err.Error())
	}
	var a graphTypeArgs
	if err := json.Unmarshal(params.Arguments, &a); err != nil {
		return true, errorResult("custom_collector: invalid arguments: " + err.Error())
	}

	switch a.Operation {
	case "register":
		return true, handleGraphTypeRegister(ctx, deps, a)
	case "update":
		return true, handleGraphTypeUpdate(ctx, deps, a)
	case "delete":
		return true, handleGraphTypeDelete(ctx, deps, a)
	case "list":
		return true, handleGraphTypeList(ctx, deps, a)
	default:
		return true, unknownOperationResult("custom_collector", a.Operation,
			[]string{"register", "update", "delete", "list"})
	}
}
