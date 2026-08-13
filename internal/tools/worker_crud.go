// SPDX-License-Identifier: Apache-2.0

// worker_crud.go — client-side intercept handlers for the worker tool's
// list/create/update/delete CRUD operations. Mirrors the trigger/status/
// running/cancel handlers in worker.go but routes through deps.WorkerCRUD()
// (a wire-loopback client) rather than the in-process dream runtime.
//
// The dispatch entry point lives in worker.go::InterceptWorker; this file
// only holds the per-op bodies.

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"

	"github.com/fulminate-io/knowledge-mcp/internal/config"
	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
	workers "github.com/fulminate-io/knowledge-mcp/internal/workers"
)

// handleWorkerList enumerates every visible worker via the wire-loopback
// CRUD client. The result is sorted by name so list output is
// deterministic regardless of map-iteration order.
func handleWorkerList(ctx context.Context, deps ClientDeps, a workerArgs) kgtools.ToolResult {
	cc := deps.WorkerCRUD()
	if cc == nil {
		return errorResult("worker:list: workerCRUD not wired — constructClient degraded at boot")
	}
	ws, err := cc.List(ctx)
	if err != nil {
		return errorResult("worker:list: " + err.Error())
	}
	sort.Slice(ws, func(i, j int) bool { return ws[i].Name < ws[j].Name })
	if a.Format == "json" {
		return jsonResult(workersAsJSON(ws))
	}
	return textResult(formatWorkersTable(ws))
}

// handleWorkerCreate registers a new graph-resident worker.
// Worker.Validate runs before the wire call so structural problems
// (empty system prompt, invalid provider, malformed cron expr) surface
// at the caller without a round-trip.
func handleWorkerCreate(ctx context.Context, deps ClientDeps, a workerArgs) kgtools.ToolResult {
	cc := deps.WorkerCRUD()
	if cc == nil {
		return errorResult("worker:create: workerCRUD not wired — constructClient degraded at boot")
	}
	name := strings.TrimSpace(a.Name)
	if name == "" {
		return errorResult("worker:create: name is required")
	}
	w, err := workerFromArgs(a)
	if err != nil {
		return errorResult("worker:create: " + err.Error())
	}
	if err := w.Validate(); err != nil {
		return errorResult("worker:create: " + err.Error())
	}
	if err := cc.Create(ctx, w); err != nil {
		return errorResult("worker:create: " + err.Error())
	}
	// Creating a worker in this process is what installs its triggers. The
	// runtime is nil in router-less / headless fixtures and when boot
	// degraded; the row is still created, it simply has no live
	// registration until a process that has a runtime creates one.
	if rt := deps.WorkerRuntime(); rt != nil {
		rt.InstallWorker(ctx, w)
	}
	return textResult(fmt.Sprintf("worker %q created (provider=%s tools=%v enabled=%v)",
		w.Name, w.Provider, w.ToolAllowlist, w.Enabled))
}

// handleWorkerUpdate edits a worker's mutable fields. Treats update as
// a full re-write: the caller supplies every field they want persisted.
// This mirrors the log_backend tool's identity-preserving Upsert
// semantics. The handler runs Worker.Validate so empty system prompts
// or unknown providers cannot land via update.
func handleWorkerUpdate(ctx context.Context, deps ClientDeps, a workerArgs) kgtools.ToolResult {
	cc := deps.WorkerCRUD()
	if cc == nil {
		return errorResult("worker:update: workerCRUD not wired — constructClient degraded at boot")
	}
	name := strings.TrimSpace(a.Name)
	if name == "" {
		return errorResult("worker:update: name is required")
	}
	w, err := workerFromArgs(a)
	if err != nil {
		return errorResult("worker:update: " + err.Error())
	}
	if err := w.Validate(); err != nil {
		return errorResult("worker:update: " + err.Error())
	}
	if err := cc.Update(ctx, w); err != nil {
		return errorResult("worker:update: " + err.Error())
	}
	return textResult(fmt.Sprintf("worker %q updated (enabled=%v)", w.Name, w.Enabled))
}

// handleWorkerDelete removes a graph-resident worker via the wire-
// loopback CRUD client. The not-found classification still works
// because workercrud.Client maps the wire ' not found' suffix back to
// graphclient.ErrNotFound (see workercrud/server.go::Delete).
func handleWorkerDelete(ctx context.Context, deps ClientDeps, a workerArgs) kgtools.ToolResult {
	cc := deps.WorkerCRUD()
	if cc == nil {
		return errorResult("worker:delete: workerCRUD not wired — constructClient degraded at boot")
	}
	name := strings.TrimSpace(a.Name)
	if name == "" {
		return errorResult("worker:delete: name is required")
	}
	if err := cc.Delete(ctx, name); err != nil {
		if errors.Is(err, graphclient.ErrNotFound) {
			return errorResult(fmt.Sprintf("worker:delete: worker %q not found (use worker(operation:\"list\") to enumerate registered workers)", name))
		}
		return errorResult("worker:delete: " + err.Error())
	}
	return textResult(fmt.Sprintf("worker %q deleted", name))
}

// workerFromArgs translates a parsed workerArgs into a workers.Worker.
// Triggers are decoded from the JSON-RawMessage carrier; absent
// triggers decode to a nil slice (the worker has no event-driven
// subscriptions and is reachable only via worker:trigger).
func workerFromArgs(a workerArgs) (workers.Worker, error) {
	w := workers.Worker{
		Name:                strings.TrimSpace(a.Name),
		Description:         a.Description,
		SystemPrompt:        a.SystemPrompt,
		Provider:            config.Provider(strings.TrimSpace(a.Provider)),
		Model:               a.Model,
		BaseURL:             a.BaseURL,
		ToolAllowlist:       []string(a.ToolAllowlist),
		MaxIterations:       int(a.MaxIterations),
		MaxWallclockSeconds: int(a.MaxWallclockSeconds),
	}
	if a.Enabled != nil {
		w.Enabled = bool(*a.Enabled)
	}
	if len(a.Triggers) > 0 {
		var triggers []workers.Trigger
		if err := json.Unmarshal(a.Triggers, &triggers); err != nil {
			return workers.Worker{}, fmt.Errorf("triggers: %w", err)
		}
		w.Triggers = triggers
	}
	return w, nil
}
