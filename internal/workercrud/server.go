// SPDX-License-Identifier: Apache-2.0

package workercrud

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"connectrpc.com/connect"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
	workers "github.com/fulminate-io/knowledge-mcp/internal/workers"
)

// workerCRUDClient is the minimal slice of *server.GraphClient that the
// Client needs to drive the worker CRUD surface over the wire-loopback
// transport. The reads/writes ride the Execute carrier seam:
// every CRUD op compiles to a declarative ExecuteRequest via engine.Compile
// and runs through Execute. *server.GraphClient satisfies the interface
// structurally; tests inject a fake.
//
// The CRUD methods route exclusively through Execute. The Execute signature
// mirrors *graphclient.GraphClient.Execute (cmd/knowledge/internal/graphclient/client.go) byte-for-byte —
// changing parameters here breaks the structural-satisfaction contract at the
// production callsite.
type workerCRUDClient interface {
	Execute(
		ctx context.Context,
		req *knowledgev1.ExecuteRequest,
	) (*knowledgev1.ExecuteResponse, error)
}

// Client is the client-side NodeWorker CRUD surface. It is wire-loopback:
// every method dispatches to the server's `query` and `mutate` tools via
// the injected workerCRUDClient (production: *server.GraphClient; tests:
// a fake). There is no in-process store-engine dependency on this side —
// the server owns the graph singleton.
//
// Client satisfies cmd/knowledge/internal/tools.WorkerCRUDAPI — the
// dispatch surface used by InterceptWorker for list/create/update/
// delete. Trigger and status remain on *dream.Runner via WorkerRuntime.
type Client struct {
	gc workerCRUDClient
}

// New returns a Client backed by gc. A nil gc is permitted but every
// method will then return an error; production callers must wire a real
// *server.GraphClient.
func New(gc workerCRUDClient) *Client { return &Client{gc: gc} }

// Wire query tool converts limit<=0 to default 20 at
// cmd/knowledge-server/tools/tools_query_dispatch.go:69-72
// (limit := int(a.Limit); if limit <= 0 { limit = 20 }), unlike the
// store-layer applyPage at domains/store/query_executor.go where
// Limit(0) means uncapped. Pass an explicit large limit here so a
// growing worker catalog doesn't silently truncate at 20. If worker
// counts ever approach 100k, switch to offset-loop pagination using
// the existing offset field on the query tool.
const workerListLimit = 100000

// List enumerates every graph-resident NodeWorker via the Execute carrier
// seam: a type=worker browse compiled by engine.Compile("query") whose typed
// Nodes carrier (engine.DecodeNodes) carries the full *knowledgev1.Node payloads,
// mapped to workers.Worker via NodeToWorker. Returns (nil, nil) when the graph
// holds no worker nodes.
func (c *Client) List(ctx context.Context) ([]workers.Worker, error) {
	if c == nil || c.gc == nil {
		return nil, errors.New("workercrud: List: nil GraphClient")
	}
	args, err := json.Marshal(map[string]any{
		"type":  "worker",
		"limit": workerListLimit,
	})
	if err != nil {
		return nil, fmt.Errorf("workercrud: List: marshal args: %w", err)
	}
	req, ok := engine.Compile("query", json.RawMessage(args))
	if !ok {
		return nil, errors.New("workercrud: List: query args not reducible to an ExecuteRequest")
	}
	resp, err := c.gc.Execute(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("workercrud: List: execute: %w", err)
	}
	nodes, err := engine.DecodeNodes(resp)
	if err != nil {
		return nil, fmt.Errorf("workercrud: List: decode nodes: %w", err)
	}
	if len(nodes) == 0 {
		return nil, nil
	}
	out := make([]workers.Worker, 0, len(nodes))
	for _, n := range nodes {
		w, err := NodeToWorker(n)
		if err != nil {
			return nil, fmt.Errorf("workercrud: List: decode %q: %w", n.GetSymbolName(), err)
		}
		out = append(out, w)
	}
	return out, nil
}

// ByName returns the Worker matching name. The bool result is false when
// no Worker carries that name. Empty name returns (zero, false, nil)
// without touching the store. Mirrors the pre-move list-then-scan impl.
func (c *Client) ByName(ctx context.Context, name string) (workers.Worker, bool, error) {
	if name == "" {
		return workers.Worker{}, false, nil
	}
	all, err := c.List(ctx)
	for _, w := range all {
		if w.Name == name {
			return w, true, err
		}
	}
	return workers.Worker{}, false, err
}

// Create writes a NEW graph-resident NodeWorker via a wire mutate(upsert) call.
// `worker` is on the engine upsert arm's type allowlist, so the body bypasses
// the create-path validation guards (summary/name) that would otherwise reject
// the write because NodeWorker is Summarizable()=false and WorkerToNode leaves
// Summary empty. That bypass is the allowlist's whole purpose — a type dropped
// from it starts running full create-time validation here.
func (c *Client) Create(ctx context.Context, w workers.Worker) error {
	return c.upsertWorker(ctx, w, "Create")
}

// Update edits an existing graph-resident worker via the same
// mutate(upsert) call — upsert is the unified create-or-update path.
func (c *Client) Update(ctx context.Context, w workers.Worker) error {
	return c.upsertWorker(ctx, w, "Update")
}

// upsertWorker is the shared body for Create and Update. The wire call
// is identical; the verb is just for error attribution.
func (c *Client) upsertWorker(ctx context.Context, w workers.Worker, verb string) error {
	if c == nil || c.gc == nil {
		return fmt.Errorf("workercrud: %s: nil GraphClient", verb)
	}
	node, err := WorkerToNode(w)
	if err != nil {
		return fmt.Errorf("workercrud: %s: WorkerToNode: %w", verb, err)
	}
	args, err := json.Marshal(map[string]any{
		"operation":   "upsert",
		"type":        "worker",
		"id":          w.Name, // node ID = worker name per WorkerToNode invariant
		"name":        w.Name, // SymbolName
		"description": w.Description,
		// Must match WorkerToNode's Source at persist.go:82. The engine UPSERT
		// arm (compileMutateUpsert) carries Source verbatim; an empty source would
		// silently change the attribution from "worker:configure" on every write.
		"source":   "worker:configure",
		"metadata": node.Metadata,
	})
	if err != nil {
		return fmt.Errorf("workercrud: %s: marshal args: %w", verb, err)
	}
	req, ok := engine.Compile("mutate", json.RawMessage(args))
	if !ok {
		return fmt.Errorf("workercrud: %s: upsert args not reducible to an ExecuteRequest", verb)
	}
	if _, err := c.gc.Execute(ctx, req); err != nil {
		return fmt.Errorf("workercrud: %s: execute: %w", verb, err)
	}
	return nil
}

// Delete removes a graph-resident worker by ID (= w.Name) via the Execute
// carrier seam: a MUTATION_KIND_DELETE by-id plan (engine delete arm — ids[]).
// A CodeNotFound engine error is mapped to a wrapped graphclient.ErrNotFound so
// errors.Is holds for the InterceptWorker delete path's classification; any
// other engine error surfaces verbatim. (A by-id delete of an absent node is a
// store-side no-op, so CodeNotFound arises only when the engine itself reports
// the miss — the mapping stays for that path + forward-compat.)
func (c *Client) Delete(ctx context.Context, name string) error {
	if c == nil || c.gc == nil {
		return errors.New("workercrud: Delete: nil GraphClient")
	}
	args, err := json.Marshal(map[string]any{
		"operation": "delete",
		"ids":       []string{name}, // engine DELETE arm selects via Selection.Ids
	})
	if err != nil {
		return fmt.Errorf("workercrud: Delete: marshal args: %w", err)
	}
	req, ok := engine.Compile("mutate", json.RawMessage(args))
	if !ok {
		return errors.New("workercrud: Delete: delete args not reducible to an ExecuteRequest")
	}
	if _, err := c.gc.Execute(ctx, req); err != nil {
		var ce *connect.Error
		if errors.As(err, &ce) && ce.Code() == connect.CodeNotFound {
			return fmt.Errorf("workercrud: Delete: %w", graphclient.ErrNotFound)
		}
		return fmt.Errorf("workercrud: Delete: execute: %w", err)
	}
	return nil
}
