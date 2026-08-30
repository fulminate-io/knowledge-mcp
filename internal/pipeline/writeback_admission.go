// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"strings"
	"sync"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// writeback_admission.go bounds how many pipeline writebacks may be IN FLIGHT
// against one server lock domain at a time.
//
// WHY A CLIENT-SIDE GATE IN FRONT OF A SERVER-SIDE MUTEX. The server already
// serializes writebacks per graph with a per-graph advisory write mutex taken as
// the writeback transaction's first statement. That mutex is the correctness
// backstop and stays exactly as it is. What it cannot do is make the excess
// writers cheap: a worker that discovers the contention INSIDE the transaction
// is holding an open server transaction and a pooled connection while it waits
// in the lock queue. Admitting here instead parks that worker on a Go channel
// holding neither. The queue does not disappear — it moves to the side of the
// wire where waiting costs nothing.
//
// THE GATE STORE'S SIZE. One entry per lock domain this client writes to, each
// one channel of capacity WritebackHoldersPerGraph. The set of domains is
// bounded by the pipeline's wanted set — tens of graphs — so the map is small
// and never evicted. Eviction is deliberately absent: proving no holder is
// mid-flight for a key would cost more code than the entry saves bytes.

// WritebackHoldersPerGraph is how many writebacks may be in flight against one
// lock domain at once.
//
// WHY 2 AND NOT 1 OR 20. The server's per-graph advisory mutex grants exactly
// ONE holder at a time, so a single permit would leave that lock idle for the
// whole client-server round trip between successive holders. A second permit
// keeps one writer's round trip overlapped with the current holder's hold —
// the minimum depth that keeps a serialized resource busy. A third adds only
// queue depth, which is the convoy this gate exists to remove. The window one
// permit covers is ONE Execute round trip, never a retry sequence; that bound
// is part of this derivation and is enforced at the acquisition site in rpc.go.
const WritebackHoldersPerGraph = 2

// admissionGates holds one permit channel per lock domain, guarded by
// admissionMu. Package-level because the domains are a property of the server
// being written to, not of any one Pipeline instance.
var (
	admissionMu    sync.Mutex
	admissionGates = make(map[graphKey]chan struct{})
)

// admissionKeyFor is the SINGLE source of the rule that maps a writeback's
// (graph type, graph name) to the server lock domain it will contend on. It
// must MIRROR the server's key, because a key that is coarser than the
// server's serializes writebacks that share no lock, and one that is finer
// admits writers the server will then queue anyway.
//
// THE CODE FAMILY KEEPS ITS OVERLAY SUFFIX; EVERY OTHER FAMILY DROPS IT. A code
// branch overlay's graph key is composed as "<type>/<base>@<overlay>", the flush
// applies under that composed key, and the advisory key is that key plus a write
// suffix — so a base repo and its branch overlay take DIFFERENT advisory locks
// and genuinely do not contend. writeBatchUpdates threads the branch through to
// the server for the code family only; for every other family the instance key
// it sends is the pre-"@" base, so the server locks the BASE graph whatever
// "@overlay" suffix the gap scan tagged the item with.
func admissionKeyFor(gt kgtypes.GraphType, graphName string) graphKey {
	if gt == kgtypes.GraphCode {
		return graphKey{GraphType: gt, GraphName: graphName}
	}
	base, _, _ := strings.Cut(graphName, "@")
	return graphKey{GraphType: gt, GraphName: base}
}

// gateFor returns the permit channel for key, creating it on first use.
func gateFor(key graphKey) chan struct{} {
	admissionMu.Lock()
	defer admissionMu.Unlock()
	g, ok := admissionGates[key]
	if !ok {
		g = make(chan struct{}, WritebackHoldersPerGraph)
		admissionGates[key] = g
	}
	return g
}

// admitWriteback blocks until a permit for key is free or ctx is done, and
// returns the closure that releases it.
//
// A ctx-cancelled acquisition still returns a NON-NIL no-op closure, so
// `defer admitWriteback(ctx, key)()` is always safe to write and a failed
// acquisition can never release a permit it does not hold. That is the same
// discipline the server-side admission gate keeps: a permit returned that was
// never taken, or taken and never returned, shrinks the budget permanently in
// one direction or removes it in the other.
func admitWriteback(ctx context.Context, key graphKey) func() {
	gate := gateFor(key)
	select {
	case gate <- struct{}{}:
		return func() { <-gate }
	case <-ctx.Done():
		return func() {}
	}
}
