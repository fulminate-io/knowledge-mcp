// SPDX-License-Identifier: Apache-2.0

// graphtype.go — client-side intercept for the `graph_type` MCP tool's
// register/update/delete/list operations. The record is a graph-resident
// config node owned by the server; every op is CRUD over it via a
// wire-loopback client (deps.GraphTypeCRUD()), mirroring the worker tool's
// list/create/update/delete handlers.
//
// The graph_type tool schema lives client-side at
// cmd/knowledge/internal/tools/graphtype_schema.go (GraphTypeToolDef);
// cmd/knowledge.loadSchemas appends it to the merged tool set so tools/list
// advertises the full op surface.

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// GraphTypeCRUDAPI is the narrow surface the graph_type handlers call on the
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
// graph_type call and should fall through. Mirrors InterceptWorker.
func InterceptGraphType(deps ClientDeps, params kgtools.CallToolParams) (bool, kgtools.ToolResult) {
	if params.Name != "graph_type" {
		return false, kgtools.ToolResult{}
	}
	var a graphTypeArgs
	if err := json.Unmarshal(params.Arguments, &a); err != nil {
		return true, errorResult("graph_type: invalid arguments: " + err.Error())
	}
	ctx := context.Background()
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
		ops := []string{"register", "update", "delete", "list"}
		return true, errorResult(fmt.Sprintf("graph_type: unknown operation %q — valid operations: %s", a.Operation, strings.Join(ops, ", ")))
	}
}
