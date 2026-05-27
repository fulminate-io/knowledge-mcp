// SPDX-License-Identifier: Apache-2.0

package cloud

import (
	"context"
	"sync"
)

// ResolutionMap records (CollectTarget.ID → CollectTarget.ResolutionID)
// pairs as cascade dispatch happens, so cascade targets that receive a
// lossy ID (e.g. AKS kubeconfig context name) can recover the canonical
// resource ID (e.g. AKS ARM path) downstream.
//
// Pattern mirrors CascadeSet: install once on a top-level context via
// WithResolutionMap, retrieve via ResolutionMapFrom, and call Record
// next to where CascadeSet.Mark is called. ResolutionMapFrom returns
// nil for naked contexts; callers must nil-check before Record/Lookup.
//
// Thread-safe: all access is protected by an RWMutex.
type ResolutionMap struct {
	mu sync.RWMutex
	m  map[string]string
}

// NewResolutionMap creates an empty ResolutionMap ready for use.
func NewResolutionMap() *ResolutionMap {
	return &ResolutionMap{m: make(map[string]string)}
}

// Record stores resolution against key. A non-empty resolution OVERWRITES
// any previous entry; an empty resolution is a no-op so cascade loops
// can call Record(t.Id, t.ResolutionID) unconditionally without
// clobbering an entry that may have been recorded by a different path.
func (r *ResolutionMap) Record(key, resolution string) {
	if resolution == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.m[key] = resolution
}

// Lookup returns the resolution for key. The bool is true when a
// non-empty resolution has been recorded for that key.
func (r *ResolutionMap) Lookup(key string) (string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	v, ok := r.m[key]
	return v, ok
}

// resolutionMapKey is the unexported context key type for ResolutionMap.
type resolutionMapKey struct{}

// WithResolutionMap returns a new context carrying the given ResolutionMap.
func WithResolutionMap(ctx context.Context, rm *ResolutionMap) context.Context {
	return context.WithValue(ctx, resolutionMapKey{}, rm)
}

// ResolutionMapFrom retrieves the ResolutionMap from the context, or nil
// if none is set. Callers must nil-check before calling Record / Lookup.
func ResolutionMapFrom(ctx context.Context) *ResolutionMap {
	rm, _ := ctx.Value(resolutionMapKey{}).(*ResolutionMap)
	return rm
}
