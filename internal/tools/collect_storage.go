// SPDX-License-Identifier: Apache-2.0

package tools

import "github.com/fulminate-io/knowledge-mcp/internal/graphclient"

// ForDestination isolates single-flight, status and completion epochs for each
// storage copy. Children share the daemon cancellation root.
func (r *CollectRuntime) ForDestination(d graphclient.Destination) *CollectRuntime {
	if r == nil || d.Storage == "" || r.destination == d {
		return r
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.destinations == nil {
		r.destinations = make(map[graphclient.Destination]*CollectRuntime)
	}
	if child := r.destinations[d]; child != nil {
		return child
	}
	child := NewCollectRuntime()
	child.baseCancel()
	child.baseCtx, child.baseCancel = r.baseCtx, nil
	child.destination = d
	child.detachAfter = r.detachAfter
	r.destinations[d] = child
	return child
}
