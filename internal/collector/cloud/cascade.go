// SPDX-License-Identifier: Apache-2.0

package cloud

import (
	"context"
	"sync"
)

// cascadeKey is a composite key identifying a (collector, id) pair for deduplication.
type cascadeKey struct {
	collector string
	id        string
}

// CascadeSet tracks which (collector, id) pairs have already been visited during
// cascade discovery. It prevents infinite loops when cloud resources reference
// each other across providers (e.g. EKS cluster -> k8s workloads -> IAM roles).
// Thread-safe: all access is protected by a mutex.
type CascadeSet struct {
	mu      sync.Mutex
	visited map[cascadeKey]struct{}
}

// NewCascadeSet creates an empty CascadeSet ready for use.
func NewCascadeSet() *CascadeSet {
	return &CascadeSet{
		visited: make(map[cascadeKey]struct{}),
	}
}

// Mark records a (collector, id) pair as visited. Returns true if this pair
// is new (caller should collect), false if it was already visited (caller should skip).
func (cs *CascadeSet) Mark(collector, id string) bool {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	key := cascadeKey{collector: collector, id: id}
	if _, ok := cs.visited[key]; ok {
		return false
	}
	cs.visited[key] = struct{}{}
	return true
}

// cascadeKeyCtx is the unexported context key type for storing a CascadeSet.
type cascadeKeyCtx struct{}

// WithCascadeSet returns a new context carrying the given CascadeSet.
func WithCascadeSet(ctx context.Context, cs *CascadeSet) context.Context {
	return context.WithValue(ctx, cascadeKeyCtx{}, cs)
}

// CascadeSetFrom retrieves the CascadeSet from the context, or nil if none is set.
func CascadeSetFrom(ctx context.Context) *CascadeSet {
	cs, _ := ctx.Value(cascadeKeyCtx{}).(*CascadeSet)
	return cs
}
