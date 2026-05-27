// SPDX-License-Identifier: Apache-2.0

package dream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// runner_control.go implements the WorkerRuntime surface used by the
// client-side worker intercept (cmd/knowledge/internal/tools/worker.go).
// *Runner satisfies WorkerRuntimeAPI structurally.
//
// Worker CRUD (List, ByName, Create, Update, Delete) does NOT live here
// — it is a client-side surface implemented by
// cmd/knowledge/internal/workercrud.Client over the wire-loopback
// transport. The runtime side of the runner is OnManualTrigger +
// Status, both of which read state the runner already owns (the
// Registry catalog and the per-worker log file) without any direct
// graph-store coupling — the Registry's worker list call is the only
// graph-bound surface and it goes over the wire-loopback transport.

// List enumerates every graph-loaded worker.
func (r *Runner) List(ctx context.Context) ([]Worker, error) {
	if r == nil || r.Registry == nil {
		return nil, errors.New("dream: Runner.List: nil Registry")
	}
	return r.Registry.All(ctx)
}

// ByName returns the Worker matching name; the bool reports whether any
// graph-loaded Worker carries that name.
func (r *Runner) ByName(ctx context.Context, name string) (Worker, bool, error) {
	if r == nil || r.Registry == nil {
		return Worker{}, false, errors.New("dream: Runner.ByName: nil Registry")
	}
	return r.Registry.ByName(ctx, name)
}

// OnManualTrigger fires a worker manually with payload. Spawns the
// invocation async and returns immediately. Worker-started and
// worker-completed events fire from inside runWorker; OnManualTrigger
// does NOT emit a Trigger.Event="manual" event (no producer in v1).
//
// The 3-arg shape (no Event wrapper) is locked per the plan — the manual
// fire path is the simplest possible "spawn this thing now" entry point;
// callers that need event-shaped context build the payload themselves.
func (r *Runner) OnManualTrigger(ctx context.Context, name string, payload json.RawMessage) error {
	if r == nil {
		return errors.New("dream: Runner.OnManualTrigger: nil receiver")
	}
	w, ok, err := r.ByName(ctx, name)
	if err != nil {
		return fmt.Errorf("dream: Runner.OnManualTrigger: ByName %q: %w", name, err)
	}
	if !ok {
		return fmt.Errorf("dream: Runner.OnManualTrigger: worker %q not found", name)
	}
	r.inFlight.Go(func() {
		r.runWorker(ctx, w, payload)
	})
	return nil
}

// Status returns the last `limit` invocation records for the named
// worker, in reverse-chronological order. Reads from the per-worker
// log file at <GraphStorage>/workers/<name>.log.
func (r *Runner) Status(_ context.Context, name string, limit int) ([]InvocationRecord, error) {
	if r == nil {
		return nil, errors.New("dream: Runner.Status: nil receiver")
	}
	if r.GraphStorage == "" {
		return nil, errors.New("dream: Runner.Status: empty GraphStorage")
	}
	return ReadRecent(r.GraphStorage, name, limit)
}
