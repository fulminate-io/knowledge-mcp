// SPDX-License-Identifier: Apache-2.0

package graphtypecrud

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

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
// call after registration validation. `graph_type_def` is on the engine upsert
// arm's type allowlist, so the body bypasses the create-path validation guards
// (summary/name) that would otherwise reject the write because
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
// It enforces the record-shape invariants via Validate, then the name rules via
// ValidateName. Update enforces the same gate as Create.
func validateRegistration(d *knowledgev1.GraphTypeDef) error {
	if err := Validate(d); err != nil {
		return err
	}
	return ValidateName(d.GetName())
}

// ValidateName rejects a family name that collides with a built-in GRAPH TYPE,
// so a registered family can never shadow one, one that names a RETIRED
// built-in, and one carrying a COLON, which is a separator in the identifiers
// derived from a family name.
//
// IT IS EXPORTED BECAUSE THE FIRST GATE IS NOW THE CLI. `knowledge collector
// add` refuses a colliding name BEFORE writing the entry; letting the file take
// it and failing at the next collect would leave an operator with a written
// entry that can never run. The upsert path runs the same function, and so does
// the config-file loader, so the three cannot drift into different answers.
//
// IT DOES NOT REFUSE A BUILT-IN COLLECTOR'S NAME, and the distinction is the
// point rather than a gap. aws, gcp, azure, k8s, github, gitlab and bitbucket
// are collector names and not graph types, so an entry under one of them is
// ADMITTED — and the collect dispatch then resolves it to the entry rather than
// to the compiled-in collector, because the entry name is the graph family. A
// registered family can shadow a built-in COLLECTOR by design; it can never
// shadow a built-in GRAPH TYPE, which is what this gate enforces.
func ValidateName(name string) error {
	if err := ValidateNameSegments(name); err != nil {
		return err
	}
	if kgtypes.IsBuiltinGraphType(name) {
		return fmt.Errorf("graphtypecrud: name %q collides with a built-in graph type", name)
	}
	// A RETIRED BUILTIN IS NOT A FREE NAME. IsBuiltinGraphType stops claiming a
	// removed family, so without this check the name becomes registrable and a
	// custom graph could adopt the leftover directory an upgrading operator still
	// has on disk — a removed family silently degrading into a registered one.
	if reason, retired := kgtypes.RetiredGraphTypeReason(name); retired {
		return fmt.Errorf("graphtypecrud: name %q names a RETIRED built-in graph type and may not be re-registered: %s",
			name, reason)
	}
	return nil
}

// ValidateNameSegments enforces the rules a family name owes the IDENTIFIERS
// DERIVED FROM IT, as opposed to the rules about which names are free to claim.
// It is the strictly shape half of ValidateName, split out so the config-file
// loader can reach it without also reaching the claim rules.
//
// A COLON IS A SEPARATOR IN AN IDENTIFIER THIS NAME BECOMES A SEGMENT OF, and
// admission is the only place it can be refused. A cross-graph proxy for a node
// in a registered family is stored under "proxy:custom/<family>:<graph>:<node
// id>"; a family carrying a colon adds a segment and shifts every field after
// it, and that id is PERSISTED, so by the time anything notices, the operator's
// own stored data is what a later refusal would be rejecting.
//
// ONLY THE FAMILY IS CONSTRAINED, and the reason is reachability rather than
// position. A NODE ID may carry colons freely and routinely does — a code node
// id is full of them — because it is the last segment. A GRAPH NAME carrying
// one is not so much harmless as UNCHANGED IN RISK: (graph "a:b", node "c")
// and (graph "a", node "b:c") already render the same id on every arm,
// including the four builtin ones, so refusing it here would fix nothing that
// the builtin arms do not equally have. The FAMILY is different: it is the one
// segment an operator names at admission, before any id exists, and it is the
// only one this predicate can reach.
//
// A SLASH IS ADMITTED: the generic proxy id's second segment carries one in
// every case, so a family name with one changes nothing about the id.
//
// WHY THE SPLIT RATHER THAN ONE FUNCTION FOR ALL THREE ROUTES. The claim rules
// answer "may this name be registered", and the config-file loader deliberately
// does NOT answer that: a client-side test proves the collect dispatch
// short-circuits a built-in graph type WITHOUT depending on any write path
// refusing it, and it builds that state by writing such an entry into a config
// file. Routing the whole of ValidateName through the loader would make that
// state unconstructible and would silently convert a client-side guarantee into
// a claim about the loader. The shape rule has no such tension: an identifier
// nobody can parse is wrong on every route.
func ValidateNameSegments(name string) error {
	if strings.Contains(name, ":") {
		return fmt.Errorf(
			"graphtypecrud: name %q contains ':', which separates the segments of the identifiers derived from a family name (a cross-graph proxy id is proxy:custom/<family>:<graph>:<node id>)", name)
	}
	return nil
}
