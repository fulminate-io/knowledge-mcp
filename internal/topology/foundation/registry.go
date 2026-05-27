// SPDX-License-Identifier: Apache-2.0

package foundation

import (
	"fmt"
	"sort"
	"sync"
)

// registry holds all analyzers registered via Register. Protected by an
// RWMutex so init() self-registration from multiple files is safe even if
// the Go runtime parallelizes package init (it doesn't today, but the
// contract should be robust).
var (
	registryMu sync.RWMutex
	registry   = map[string]Analyzer{}
)

// Register adds an analyzer to the global registry. Analyzers normally
// self-register from init() in their own file. Panics if a is nil, if
// a.Name() is empty, or if another analyzer is already registered under
// the same name — all three are programmer errors, not runtime
// conditions. This mirrors the convention used by net/http.Handle and
// database/sql.Register.
func Register(a Analyzer) {
	if a == nil {
		panic("topology: Register called with nil Analyzer")
	}
	name := a.Name()
	if name == "" {
		panic("topology: Register called with empty Analyzer.Name()")
	}
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, dup := registry[name]; dup {
		panic(fmt.Sprintf("topology: duplicate analyzer registration: %q", name))
	}
	registry[name] = a
}

// Get returns the registered analyzer with the given name and a boolean
// indicating whether it was found.
func Get(name string) (Analyzer, bool) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	a, ok := registry[name]
	return a, ok
}

// All returns every registered analyzer sorted by Name. The returned
// slice is a snapshot — callers may iterate it without holding the
// registry lock and without observing concurrent registrations.
func All() []Analyzer {
	registryMu.RLock()
	out := make([]Analyzer, 0, len(registry))
	for _, a := range registry {
		out = append(out, a)
	}
	registryMu.RUnlock()
	sort.Slice(out, func(i, j int) bool {
		return out[i].Name() < out[j].Name()
	})
	return out
}
