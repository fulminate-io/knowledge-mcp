// SPDX-License-Identifier: Apache-2.0

package graphtypecrud

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"connectrpc.com/connect"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// graphTypeCRUDClient is the minimal slice of *server.GraphClient that the
// Client needs to drive the graph-type CRUD surface over the wire-loopback
// transport. Every CRUD op compiles to a declarative ExecuteRequest via
// engine.Compile and runs through Execute. *server.GraphClient satisfies the
// interface structurally; tests inject a fake. The Execute signature mirrors
// *graphclient.GraphClient.Execute byte-for-byte — changing parameters here
// breaks the structural-satisfaction contract at the production callsite.
type graphTypeCRUDClient interface {
	Execute(
		ctx context.Context,
		req *knowledgev1.ExecuteRequest,
	) (*knowledgev1.ExecuteResponse, error)
}

// Client is the client-side NodeGraphTypeDef CRUD surface. It is wire-loopback:
// every method dispatches to the server's `query` and `mutate` tools via the
// injected graphTypeCRUDClient (production: *server.GraphClient; tests: a fake).
// There is no in-process store-engine dependency on this side — the server owns
// the graph singleton. The record type is the gen *knowledgev1.GraphTypeDef and
// the codec is the same-package ToNode/FromNode.
type Client struct {
	gc graphTypeCRUDClient
}

// New returns a Client backed by gc. A nil gc is permitted but every method will
// then return an error; production callers must wire a real *server.GraphClient.
func New(gc graphTypeCRUDClient) *Client { return &Client{gc: gc} }

// graphTypeListLimit mirrors workercrud's large explicit limit: the wire query
// tool defaults limit<=0 to 20, so a growing graph-type catalog could silently
// truncate. A config catalog is tiny; a single large-limit browse (no N+1) is
// the right read shape.
const graphTypeListLimit = 100000

// List enumerates every graph-resident NodeGraphTypeDef via the Execute carrier
// seam: a type=graph_type_def browse whose typed Nodes carrier carries the full
// *knowledgev1.Node payloads, mapped back to *knowledgev1.GraphTypeDef via
// FromNode. Returns (nil, nil) when no graph-type nodes exist.
func (c *Client) List(ctx context.Context) ([]*knowledgev1.GraphTypeDef, error) {
	if c == nil || c.gc == nil {
		return nil, errors.New("graphtypecrud: List: nil GraphClient")
	}
	args, err := json.Marshal(map[string]any{
		"type":  string(kgtypes.NodeGraphTypeDef),
		"limit": graphTypeListLimit,
	})
	if err != nil {
		return nil, fmt.Errorf("graphtypecrud: List: marshal args: %w", err)
	}
	req, ok := engine.Compile("query", json.RawMessage(args))
	if !ok {
		return nil, errors.New("graphtypecrud: List: query args not reducible to an ExecuteRequest")
	}
	resp, err := c.gc.Execute(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("graphtypecrud: List: execute: %w", err)
	}
	nodes, err := engine.DecodeNodes(resp)
	if err != nil {
		return nil, fmt.Errorf("graphtypecrud: List: decode nodes: %w", err)
	}
	if len(nodes) == 0 {
		return nil, nil
	}
	out := make([]*knowledgev1.GraphTypeDef, 0, len(nodes))
	for _, n := range nodes {
		d, err := FromNode(n)
		if err != nil {
			return nil, fmt.Errorf("graphtypecrud: List: decode %q: %w", n.GetSymbolName(), err)
		}
		out = append(out, d)
	}
	return out, nil
}

// ByName returns the GraphTypeDef matching name. The bool result is false when
// no record carries that name. Empty name returns (nil, false, nil) without
// touching the store. List-then-scan mirrors the worker idiom.
func (c *Client) ByName(ctx context.Context, name string) (*knowledgev1.GraphTypeDef, bool, error) {
	if name == "" {
		return nil, false, nil
	}
	all, err := c.List(ctx)
	for _, d := range all {
		if d.GetName() == name {
			return d, true, err
		}
	}
	return nil, false, err
}

// Create writes a NEW graph-resident NodeGraphTypeDef via a wire mutate(upsert)
// call after registration validation. The underlying handleMutateUpsert handler
// bypasses the create-path validation guards (validateSummary/validateName/
// findExistingByName) that would otherwise reject the write because
// NodeGraphTypeDef is Summarizable()=false and ToNode leaves Summary empty.
func (c *Client) Create(ctx context.Context, d *knowledgev1.GraphTypeDef) error {
	return c.upsert(ctx, d, "Create")
}

// Update edits an existing graph-resident record via the same mutate(upsert)
// call — upsert is the unified create-or-update path. Update enforces the SAME
// registration validation as Create so an update cannot relax invariants.
func (c *Client) Update(ctx context.Context, d *knowledgev1.GraphTypeDef) error {
	return c.upsert(ctx, d, "Update")
}

// upsert is the shared body for Create and Update. The wire call is identical;
// the verb is only for error attribution. validateRegistration runs first so a
// malformed or built-in-colliding record never reaches the store.
func (c *Client) upsert(ctx context.Context, d *knowledgev1.GraphTypeDef, verb string) error {
	if c == nil || c.gc == nil {
		return fmt.Errorf("graphtypecrud: %s: nil GraphClient", verb)
	}
	if err := validateRegistration(d); err != nil {
		return fmt.Errorf("graphtypecrud: %s: %w", verb, err)
	}
	node, err := ToNode(d, d.GetName())
	if err != nil {
		return fmt.Errorf("graphtypecrud: %s: ToNode: %w", verb, err)
	}
	args, err := json.Marshal(map[string]any{
		"operation": "upsert",
		"type":      string(kgtypes.NodeGraphTypeDef),
		"id":        d.GetName(), // node ID = name per ToNode invariant
		"name":      d.GetName(), // SymbolName
		// Source must match ToNode's graphTypeSource; the engine UPSERT arm
		// carries Source verbatim, so an empty source would silently change the
		// attribution on every write.
		"source":   graphTypeSource,
		"metadata": node.Metadata,
	})
	if err != nil {
		return fmt.Errorf("graphtypecrud: %s: marshal args: %w", verb, err)
	}
	req, ok := engine.Compile("mutate", json.RawMessage(args))
	if !ok {
		return fmt.Errorf("graphtypecrud: %s: upsert args not reducible to an ExecuteRequest", verb)
	}
	if _, err := c.gc.Execute(ctx, req); err != nil {
		return fmt.Errorf("graphtypecrud: %s: execute: %w", verb, err)
	}
	return nil
}

// Delete removes a graph-resident record by ID (= name) via the Execute carrier
// seam: a by-id delete plan. A CodeNotFound engine error is mapped to a wrapped
// graphclient.ErrNotFound so errors.Is holds for the tool delete path's
// classification; any other engine error surfaces verbatim.
func (c *Client) Delete(ctx context.Context, name string) error {
	if c == nil || c.gc == nil {
		return errors.New("graphtypecrud: Delete: nil GraphClient")
	}
	args, err := json.Marshal(map[string]any{
		"operation": "delete",
		"ids":       []string{name},
	})
	if err != nil {
		return fmt.Errorf("graphtypecrud: Delete: marshal args: %w", err)
	}
	req, ok := engine.Compile("mutate", json.RawMessage(args))
	if !ok {
		return errors.New("graphtypecrud: Delete: delete args not reducible to an ExecuteRequest")
	}
	if _, err := c.gc.Execute(ctx, req); err != nil {
		var ce *connect.Error
		if errors.As(err, &ce) && ce.Code() == connect.CodeNotFound {
			return fmt.Errorf("graphtypecrud: Delete: %w", graphclient.ErrNotFound)
		}
		return fmt.Errorf("graphtypecrud: Delete: execute: %w", err)
	}
	return nil
}

// validateRegistration is the registration-time gate run by Create and Update.
// It (1) enforces the record-shape invariants via Validate, then (2) rejects a
// Name that collides with a built-in GraphType so a registered type can never
// shadow a built-in. Update enforces the same gate as Create.
func validateRegistration(d *knowledgev1.GraphTypeDef) error {
	if err := Validate(d); err != nil {
		return err
	}
	if kgtypes.IsBuiltinGraphType(d.GetName()) {
		return fmt.Errorf("graphtypecrud: name %q collides with a built-in graph type", d.GetName())
	}
	return nil
}
