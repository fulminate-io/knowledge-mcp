// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"sync"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// nudgeState is the Manager's RECORD-AND-WAKE set for the publish coverage gate:
// the graphs whose publish gate became unsatisfiable since the last drain, plus a
// capacity-1 channel a periodic consumer selects on so it can look sooner than its
// own cadence would. It is reached through exactly ONE Manager field so the whole
// mechanism lives in this file.
//
// THE COALESCING IS THE SHAPE. The set is a map, and the channel holds ONE token:
//
//   - Many recordings between two consumer wakes collapse to a single readable
//     wake (the second and later sends hit the non-blocking send's default arm).
//   - A recording made while nobody is receiving never blocks the recording path,
//     which runs inside a publish attempt and must not stall on a consumer.
//   - The set is drained ON READ (TakeReconcileNudges), so a consumed entry cannot
//     re-fire forever.
//
// ONE KEY, SEVERAL WRITERS. The recorder is keyed by graphKey, and a single graph
// has THREE distManagers that can reach the publish coverage gate: the embed HNSW
// engine, the deterministic HNSW engine, and the BM25 engine. Each keeps its OWN
// skip streak, so each can independently cross its own suppression transition for
// the SAME (graph type, name) — and the map collapses those into one entry. So
// "one nudge per suppression episode" is a per-KEY property, never a per-engine
// one.
//
// The map and the channel are created LAZILY under mu on first use rather than at
// Manager construction, so a zero-valued nudgeState is immediately usable.
type nudgeState struct {
	mu     sync.Mutex
	graphs map[graphKey]struct{}
	wake   chan struct{} // buffered, capacity 1 — see the coalescing note above.
}

// ensure creates the set and the wake channel on first use. The caller MUST hold
// mu. The channel in particular cannot be left at its zero value: a receive on a
// nil channel blocks forever, so a consumer that took the channel before the first
// recording would never wake.
func (n *nudgeState) ensure() {
	if n.graphs == nil {
		n.graphs = make(map[graphKey]struct{})
	}
	if n.wake == nil {
		n.wake = make(chan struct{}, 1)
	}
}

// NudgedGraph identifies one graph that asked for an earlier reconcile look.
type NudgedGraph struct {
	GraphType kgtypes.GraphType
	Name      string
}

// ReconcileNudge returns the receive end of the wake channel. A periodic reconcile
// consumer selects on it alongside its own ticker: a receive means at least one
// graph's publish coverage gate went unsatisfiable, and the consumer should call
// TakeReconcileNudges and run its pass now rather than at the next tick. The
// channel is shared by every graph this Manager owns — it signals THAT work was
// recorded, not WHICH; the set carries the identities.
func (m *Manager) ReconcileNudge() <-chan struct{} {
	m.nudges.mu.Lock()
	defer m.nudges.mu.Unlock()
	m.nudges.ensure()
	return m.nudges.wake
}

// TakeReconcileNudges DRAINS the recorded set and returns it. Draining on read is
// what keeps a consumed recording from re-firing: the next wake reflects only
// graphs recorded since this call. Returns an empty slice when nothing is pending,
// which is the ordinary case for a consumer woken by its own ticker.
func (m *Manager) TakeReconcileNudges() []NudgedGraph {
	m.nudges.mu.Lock()
	defer m.nudges.mu.Unlock()
	out := make([]NudgedGraph, 0, len(m.nudges.graphs))
	for k := range m.nudges.graphs {
		out = append(out, NudgedGraph{GraphType: k.graphType, Name: k.graphName})
	}
	clear(m.nudges.graphs)
	return out
}

// flagReconcileNudge records that (gt, name) needs an earlier reconcile look and
// wakes the consumer. It is the recorder half of the mechanism, wired onto every
// distManager at construction (manager_factory.go) and invoked from the publish
// gate's suppression transition.
//
// It RECORDS AND WAKES ONLY. No rebuild — or anything that drives one — runs from
// here: rebuild ownership stays entirely with the consumer, so the existing
// single-flight and no-progress bounds remain the only rebuild entry points. The
// cost is one map insert plus one non-blocking send, and the send never blocks
// even when no consumer is running.
func (m *Manager) flagReconcileNudge(gt kgtypes.GraphType, name string) {
	m.nudges.mu.Lock()
	m.nudges.ensure()
	m.nudges.graphs[graphKey{graphType: gt, graphName: name}] = struct{}{}
	wake := m.nudges.wake
	m.nudges.mu.Unlock()

	// Non-blocking: a token already queued IS the coalescing — the consumer will
	// drain the whole set on its next wake, so a second token would only cause a
	// redundant pass.
	select {
	case wake <- struct{}{}:
	default:
	}
}
