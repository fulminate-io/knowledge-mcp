// SPDX-License-Identifier: Apache-2.0

// Package postpopulate holds the name-keyed registry of PostPopulate hooks
// that collectors install at init() time. PostPopulate derives higher-level
// cross-node / cross-graph structural edges (cloud SG/NACL rules, cross-account
// trust, cross-VPC references, image lineage, k8s selector/cluster linkage, CICD
// OIDC federation, codesync hierarchy) AFTER the collector's nodes have been
// uploaded to the named per-account/per-repo graph.
//
// Hooks are keyed by collector type (the same key the collect tool dispatches
// on — "aws", "gcp", "k8s", "github", "code", ...) and fired on the LIVE collect
// path as a tail-call sibling to the cross-graph linker
// (tools.runPostCollectPostPopulate). Each hook takes a GraphCaller (the
// Execute-only wire seam, wire.go) + a graph name, and reads/writes the graph
// ENTIRELY over the wire — the client holds no in-process store engine. A hook is fired once
// per enumerated graph of its family's type and silently no-ops (content-based)
// for a graph whose nodes do not match the family.
package postpopulate

import (
	"context"
	"sync"
)

// Func is the wire-shape PostPopulate hook signature. graphName is the named
// per-account (cloud/cicd) or per-repo (code) graph the hook reads + writes;
// the GraphCaller's selectorArgs translation (wire.go) routes the read/write to
// the right backing DB. A hook is fired once per graph of its family's type.
type Func func(ctx context.Context, gc GraphCaller, graphName string) error

var (
	mu     sync.RWMutex
	byName = map[string]Func{}
)

// Register installs a PostPopulate hook for the given collector name.
// Collisions overwrite; last registration wins (so tests can swap hooks
// in and out). Panics on empty name or nil hook to catch bugs early.
func Register(collectorName string, f Func) {
	if collectorName == "" {
		panic("postpopulate: Register called with empty collector name")
	}
	if f == nil {
		panic("postpopulate: Register called with nil hook")
	}
	mu.Lock()
	defer mu.Unlock()
	byName[collectorName] = f
}

// Lookup returns the PostPopulate hook for collectorName, or false when
// unregistered.
func Lookup(collectorName string) (Func, bool) {
	mu.RLock()
	defer mu.RUnlock()
	f, ok := byName[collectorName]
	return f, ok
}
