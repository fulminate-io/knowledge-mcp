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
// ENTIRELY over the wire — the client holds no in-process store engine.
//
// Breadth is DECLARED per hook at registration rather than assumed by the
// caller. A BreadthFamilyBroad hook is fired once per enumerated graph of its
// family's type and silently no-ops (content-based) for a graph whose nodes do
// not match the family. A BreadthScoped hook is fired exactly once, against the
// graph that was just collected.
package postpopulate

import (
	"context"
	"sync"
)

// Func is the wire-shape PostPopulate hook signature. graphName is the named
// per-account (cloud/cicd) or per-repo (code) graph the hook reads + writes;
// the GraphCaller's selectorArgs translation (wire.go) routes the read/write to
// the right backing DB. A BROAD hook is fired once per graph of its family's
// type; a SCOPED hook is fired once, against the collected graph.
type Func func(ctx context.Context, gc GraphCaller, graphName string) error

// Breadth is how widely the post-collect orchestrator fires a registered hook.
// It is declared at registration time, so the duty of knowing a hook's breadth
// stays with the registry and the orchestrator: a hook is never asked to defend
// itself against being fired against a graph it does not own.
type Breadth string

const (
	// BreadthScoped fires the hook exactly once, against the graph that was
	// just collected. A code collect produces exactly one graph, so the code
	// hook declares this.
	BreadthScoped Breadth = "scoped-to-collected-graph"
	// BreadthFamilyBroad fires the hook once per enumerated graph of the
	// family's type. A single cloud collect can cascade several provider
	// graphs, so the cloud and cicd hooks declare this.
	BreadthFamilyBroad Breadth = "family-broad"
)

// Hook is one registered entry: the hook body plus the breadth it declared.
type Hook struct {
	Fn      Func
	Breadth Breadth
}

var (
	mu     sync.RWMutex
	byName = map[string]Hook{}
)

// Register installs a PostPopulate hook for the given collector name under its
// DECLARED breadth. Collisions overwrite; last registration wins (so tests can
// swap hooks in and out). Panics on empty name, nil hook, or a breadth that is
// neither locked constant, to catch bugs early: a collector family cannot
// register without stating a breadth (the compiler forbids it) and cannot state
// an unrecognized one (this panics at init).
func Register(collectorName string, breadth Breadth, f Func) {
	if collectorName == "" {
		panic("postpopulate: Register called with empty collector name")
	}
	if f == nil {
		panic("postpopulate: Register called with nil hook")
	}
	if breadth != BreadthScoped && breadth != BreadthFamilyBroad {
		panic("postpopulate: Register called with unknown breadth: " + string(breadth))
	}
	mu.Lock()
	defer mu.Unlock()
	byName[collectorName] = Hook{Fn: f, Breadth: breadth}
}

// Lookup returns the registered Hook for collectorName, or false when
// unregistered.
func Lookup(collectorName string) (Hook, bool) {
	mu.RLock()
	defer mu.RUnlock()
	h, ok := byName[collectorName]
	return h, ok
}
