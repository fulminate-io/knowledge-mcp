// SPDX-License-Identifier: Apache-2.0

package dream

import (
	"context"

	workers "github.com/fulminate-io/knowledge-mcp/internal/workers"
)

// workerLister is the narrow slice of *workercrud.Client that Registry
// needs to enumerate graph-resident workers. *workercrud.Client
// satisfies it structurally (its List method returns []workers.Worker
// directly); tests inject a fake.
//
// Registry has no responsibility for the wire shape — workercrud.Client
// is itself wire-loopback (List dispatches to the server's `query`
// tool under the hood), but dream.Registry only sees the decoded
// workers.Worker slice. The wire decoder lives in workercrud, the only
// place that knows the wire shape; dream stays a pure data consumer.
type workerLister interface {
	List(ctx context.Context) ([]workers.Worker, error)
}

// Registry is the catalog of workers visible to the Runner. It exposes
// a snapshot of NodeWorker entries loaded from the knowledge graph via
// the wire-loopback CRUD client.
//
// The Registry holds a workerLister (production passes *workercrud.Client; tests
// inject a fake). workercrud.Client.List routes through the generic `query` tool.
type Registry struct {
	lister workerLister
}

// NewRegistry returns a Registry that loads graph-resident NodeWorker
// entries via the wire-loopback CRUD client. The lister is the
// in-process *workercrud.Client handle wired in cmd/knowledge/dream.go::
// buildRuntime; tests pass a fake satisfying workerLister.
//
// A nil lister is permitted — All / ByName short-circuit to (nil, nil)
// so test harnesses that never exercise the loader can construct a
// Registry without standing up a transport.
func NewRegistry(lister workerLister) *Registry {
	return &Registry{lister: lister}
}

// All returns the catalog of workers loaded from the graph.
//
// Errors loading from the graph are surfaced to the caller; on error,
// All returns whatever partial list it could decode so the Runner can
// boot with a degraded catalog rather than refuse to start.
func (r *Registry) All(ctx context.Context) ([]Worker, error) {
	if r == nil || r.lister == nil {
		return nil, nil
	}
	return r.lister.List(ctx)
}

// ByName returns the Worker matching name. The bool result is false
// when no Worker carries that name.
//
// Errors loading from the graph are surfaced. When err is non-nil,
// ByName still returns whatever it could resolve — same degraded-but-
// functional contract as All.
func (r *Registry) ByName(ctx context.Context, name string) (Worker, bool, error) {
	if name == "" {
		return Worker{}, false, nil
	}
	graphWorkers, err := r.All(ctx)
	for _, w := range graphWorkers {
		if w.Name == name {
			return w, true, err
		}
	}
	return Worker{}, false, err
}
