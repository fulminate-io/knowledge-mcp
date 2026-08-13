// SPDX-License-Identifier: Apache-2.0

// worker.go — client-side intercept for the `worker` MCP tool's trigger
// and status operations. The dream Runner lives in the client process
// (see Phase G's wireWorkerRuntime), so manual trigger and status reads
// must dispatch in-process here. Other worker operations (list, create,
// update, delete) are CRUD over graph-resident worker rows owned by the
// server; they fall through unchanged to the server-side handler.
//
// Mirrors the existing manage(status) / collect / ast intercept pattern
// wired from cmd/knowledge/mcp.go. The worker tool schema lives
// client-side at cmd/knowledge/internal/tools/worker_schema.go
// (WorkerToolDef); cmd/knowledge.loadSchemas appends it to the merged
// tool set so tools/list still advertises the full op surface.
//
// Test seam: WorkerRuntimeAPI is declared in this file (not in deps.go,
// not in domains/dream) so worker_test.go can satisfy it with a plain
// fakeRuntime. *dream.Runner satisfies it structurally, so the
// production wiring needs no adapter.

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"

	"github.com/fulminate-io/knowledge-mcp/internal/dream"
	workers "github.com/fulminate-io/knowledge-mcp/internal/workers"
)

// WorkerRuntimeAPI is the narrow surface InterceptWorker calls on the
// client-side dream runtime. *dream.Runner satisfies this interface
// structurally; tests inject a fakeRuntime that records calls without
// spinning up a real Runner. Keeping the interface in this file (rather
// than alongside ClientDeps in deps.go) means tests live next to the
// production code that defines the contract.
type WorkerRuntimeAPI interface {
	// InstallWorker subscribes a newly created worker's triggers. It is
	// called on a successful worker:create in this process — the only path
	// that installs triggers, since a running trigger registration is
	// per-process state and is never restored from persisted worker rows.
	InstallWorker(ctx context.Context, w dream.Worker)
	OnManualTrigger(ctx context.Context, name string, payload json.RawMessage) error
	Status(ctx context.Context, name string, limit int) ([]dream.InvocationRecord, error)
	ByName(ctx context.Context, name string) (dream.Worker, bool, error)
	Running() []dream.RunningInvocation
	Cancel(invocation, name string) (int, error)
}

// WorkerCRUDAPI is the narrow surface InterceptWorker calls on the
// client-side wire-loopback CRUD client. *workercrud.Client satisfies
// this interface structurally; tests inject a fake. Mirrors the shape
// of the old server-side WorkerCRUD interface (List/ByName/Create/
// Update/Delete) so callsite ergonomics carry over verbatim. ByName is
// included because handleWorkerStatus / handleWorkerCreate validate
// against the same lookup the dream runtime uses; reusing it here
// avoids a second list-then-scan.
type WorkerCRUDAPI interface {
	List(ctx context.Context) ([]workers.Worker, error)
	ByName(ctx context.Context, name string) (workers.Worker, bool, error)
	Create(ctx context.Context, w workers.Worker) error
	Update(ctx context.Context, w workers.Worker) error
	Delete(ctx context.Context, name string) error
}

// InterceptWorker is the entry point invoked by mcp.go's intercept chain.
// Returns (true, result) when the call was handled; (false, zero) when
// the call should fall through to the server. Mirrors InterceptAst's
// shape.
//
// Operation routing:
//   - trigger: handled here. Reads the client-side runtime via
//     deps.WorkerRuntime() and dispatches via OnManualTrigger.
//   - status:  handled here. Reads recent invocation records via Status.
//   - list / create / update / delete / unknown: fall through to the
//     server. The server still owns CRUD over graph-resident worker
//     rows (Phase D's workercrud package); only the live runtime ops
//     run client-side because the runtime itself lives in the client.
func InterceptWorker(ctx context.Context, deps ClientDeps, params kgtools.CallToolParams) (bool, kgtools.ToolResult) {
	if params.Name != "worker" {
		return false, kgtools.ToolResult{}
	}
	if err := rejectUndeclaredParams("worker", "", WorkerToolDef().InputSchema.Properties, params.Arguments); err != nil {
		return true, errorResult(err.Error())
	}
	var a workerArgs
	if err := json.Unmarshal(params.Arguments, &a); err != nil {
		// Malformed args are fatal regardless of which op was intended;
		// surface here rather than letting the server reparse.
		return true, errorResult("worker: invalid arguments: " + err.Error())
	}

	switch a.Operation {
	case "list":
		return true, handleWorkerList(ctx, deps, a)
	case "create":
		return true, handleWorkerCreate(ctx, deps, a)
	case "update":
		return true, handleWorkerUpdate(ctx, deps, a)
	case "delete":
		return true, handleWorkerDelete(ctx, deps, a)
	case "trigger":
		return true, handleWorkerTrigger(ctx, deps, a)
	case "status":
		return true, handleWorkerStatus(ctx, deps, a)
	case "running":
		return true, handleWorkerRunning(ctx, deps, a)
	case "cancel":
		return true, handleWorkerCancel(ctx, deps, a)
	default:
		// The server has no worker handler at all — the only
		// way "unknown operation" surfaces is through this client-side
		// dispatcher, so we own the error message.
		return true, unknownOperationResult("worker", a.Operation,
			[]string{"list", "create", "update", "delete", "trigger", "status", "running", "cancel"})
	}
}

// handleWorkerTrigger fires a worker manually via the client-side
// runtime. Errors when the runtime is nil (wireWorkerRuntime degraded
// at boot — see buildClient in the serve daemon bootstrap) or when the
// worker name is empty.
// The runtime itself rejects unknown / disabled workers with a
// descriptive error message; we forward that verbatim.
//
// Output shape mirrors the server-side handleWorkerTrigger so callers
// see the same "triggered (running asynchronously...)" string regardless
// of which side handled the dispatch.
func handleWorkerTrigger(ctx context.Context, deps ClientDeps, a workerArgs) kgtools.ToolResult {
	name := strings.TrimSpace(a.Name)
	if name == "" {
		return errorResult("worker:trigger: name is required")
	}
	// Readiness gate (bind-first startup): during the bind-first wiring window the worker
	// runtime is not yet wired. Distinguish that transient window from the
	// permanent boot-degrade case below — emit "daemon still starting" so a
	// retry succeeds, rather than the misleading "degraded at boot" message.
	if !deps.WorkerReady() {
		return errorResult("worker:trigger: daemon still starting — worker runtime not ready yet, retry shortly")
	}
	rt := deps.WorkerRuntime()
	if rt == nil {
		return errorResult("worker:trigger: dream runtime not available — wireWorkerRuntime degraded at boot (check earlier slog warnings for cause)")
	}
	payload := a.Payload
	if len(payload) == 0 {
		payload = json.RawMessage(`null`)
	}
	if err := rt.OnManualTrigger(ctx, name, payload); err != nil {
		return errorResult("worker:trigger: " + err.Error())
	}
	return textResult(fmt.Sprintf("worker %q triggered (running asynchronously — use worker:status to inspect)", name))
}

// handleWorkerStatus returns recent invocation records for a worker.
// Limit defaults to 10 to match the server-side handler. Output is the
// same JSON-marshaled []dream.InvocationRecord the server returned, so
// downstream consumers don't have to branch on which side handled the
// call.
func handleWorkerStatus(ctx context.Context, deps ClientDeps, a workerArgs) kgtools.ToolResult {
	name := strings.TrimSpace(a.Name)
	if name == "" {
		return errorResult("worker:status: name is required")
	}
	// Readiness gate (bind-first startup): see handleWorkerTrigger — transient wiring
	// window vs permanent boot-degrade.
	if !deps.WorkerReady() {
		return errorResult("worker:status: daemon still starting — worker runtime not ready yet, retry shortly")
	}
	rt := deps.WorkerRuntime()
	if rt == nil {
		return errorResult("worker:status: dream runtime not available — wireWorkerRuntime degraded at boot (check earlier slog warnings for cause)")
	}
	// Pre-flight existence check so a misspelled worker name surfaces as
	// "not found" instead of the silent "null" output produced when
	// ReadRecent finds no log file. ReadRecent can't distinguish "worker
	// exists, never invoked" from "worker doesn't exist"; the registry can.
	if _, found, byNameErr := rt.ByName(ctx, name); byNameErr != nil {
		return errorResult("worker:status: " + byNameErr.Error())
	} else if !found {
		return errorResult(fmt.Sprintf("worker:status: worker %q not found (use worker(operation:\"list\") to enumerate registered workers)", name))
	}
	limit := int(a.Limit)
	if limit <= 0 {
		limit = 10
	}
	records, err := rt.Status(ctx, name, limit)
	if err != nil {
		return errorResult("worker:status: " + err.Error())
	}
	return jsonResult(records)
}

// handleWorkerRunning returns the live in-flight invocation registry as
// JSON. Empty list means nothing is currently running. Operators read
// invocation_id from this output to call worker(operation:"cancel",
// invocation:"<id>") against a specific run.
func handleWorkerRunning(_ context.Context, deps ClientDeps, _ workerArgs) kgtools.ToolResult {
	// Readiness gate (bind-first startup): see handleWorkerTrigger — transient wiring
	// window vs permanent boot-degrade.
	if !deps.WorkerReady() {
		return errorResult("worker:running: daemon still starting — worker runtime not ready yet, retry shortly")
	}
	rt := deps.WorkerRuntime()
	if rt == nil {
		return errorResult("worker:running: dream runtime not available — wireWorkerRuntime degraded at boot (check earlier slog warnings for cause)")
	}
	return jsonResult(rt.Running())
}

// handleWorkerCancel cancels in-flight invocations. Either invocation
// (specific run) or name (every run of that worker) is required; passing
// both prefers invocation. Cancel propagates through the per-invocation
// context the runWorker goroutine holds, including in-flight tool calls.
// Returns the count cancelled. Canceling a finished or unknown id
// returns 0 without error (idempotent).
func handleWorkerCancel(_ context.Context, deps ClientDeps, a workerArgs) kgtools.ToolResult {
	// Readiness gate (bind-first startup): see handleWorkerTrigger — transient wiring
	// window vs permanent boot-degrade.
	if !deps.WorkerReady() {
		return errorResult("worker:cancel: daemon still starting — worker runtime not ready yet, retry shortly")
	}
	rt := deps.WorkerRuntime()
	if rt == nil {
		return errorResult("worker:cancel: dream runtime not available — wireWorkerRuntime degraded at boot (check earlier slog warnings for cause)")
	}
	invocation := strings.TrimSpace(a.Invocation)
	name := strings.TrimSpace(a.Name)
	if invocation == "" && name == "" {
		return errorResult("worker:cancel: invocation or name is required")
	}
	count, err := rt.Cancel(invocation, name)
	if err != nil {
		return errorResult("worker:cancel: " + err.Error())
	}
	if count == 0 {
		if invocation != "" {
			return textResult(fmt.Sprintf("no in-flight invocation matched id %q (already finished or unknown)", invocation))
		}
		return textResult(fmt.Sprintf("no in-flight invocation found for worker %q", name))
	}
	target := invocation
	if target == "" {
		target = "worker=" + name
	}
	return textResult(fmt.Sprintf("cancelled %d invocation(s) (%s)", count, target))
}
